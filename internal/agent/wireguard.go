package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/config"
	"github.com/shuricksumy/trapdoor/internal/httpx"
	"github.com/shuricksumy/trapdoor/internal/pki"
	"github.com/shuricksumy/trapdoor/internal/wg"
)

// wgState is a WireGuard server as stored in <dir>/<id>.json. The
// interface's .conf is rendered from it; client private keys are kept so
// their configuration can be downloaded again.
type wgState struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	PrivateKey string         `json:"private_key"`
	Address    netip.Prefix   `json:"address"` // the server's address in the VPN subnet, e.g. 10.9.0.1/24
	ListenPort int            `json:"listen_port"`
	PublicHost string         `json:"public_host"`
	PublicPort int            `json:"public_port"`
	DNS        []string       `json:"dns,omitempty"`
	MTU        int            `json:"mtu,omitempty"`
	AllowedIPs []netip.Prefix `json:"allowed_ips"`
	Keepalive  int            `json:"keepalive"`
	Peers      []wgPeer       `json:"peers"`
}

type wgPeer struct {
	Name       string         `json:"name"`
	PrivateKey string         `json:"private_key"`
	PublicKey  string         `json:"public_key"`
	PSK        string         `json:"preshared_key,omitempty"`
	Address    netip.Addr     `json:"address"`
	LAN        []netip.Prefix `json:"lan,omitempty"`
	Disabled   bool           `json:"disabled,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

// WGServer is one WireGuard server managed by the agent.
type WGServer struct {
	dir  string
	mode string // direct | file
	log  *slog.Logger
	run  wgRunner
	now  func() time.Time

	mu       sync.Mutex
	st       wgState
	applied  netip.Prefix // address the interface was brought up with
	applyErr string
	sess     map[string]*wgSession // by public key, while connected
	last     map[string][2]int64   // last rx/tx seen per public key
}

type wgSession struct {
	since          time.Time
	baseRx, baseTx int64
}

// ipForwardPath turns on IPv4 forwarding for direct mode (tests change it).
var ipForwardPath = "/proc/sys/net/ipv4/ip_forward"

// Defaults for new WireGuard servers.
const (
	wgDefaultPort      = 51820
	wgDefaultSubnet    = "10.9.0.0/24"
	wgDefaultKeepalive = 25
)

func newWGServer(cfg config.Agent, log *slog.Logger, st wgState) *WGServer {
	s := &WGServer{dir: cfg.WireGuardDir, mode: cfg.WireGuard, log: log.With("server", st.ID), st: st,
		now: time.Now, sess: map[string]*wgSession{}, last: map[string][2]int64{}}
	if cfg.WireGuard == "file" {
		s.run = wgFile{dir: cfg.WireGuardDir, now: time.Now}
	} else {
		s.run = wgDirect{toolsDir: cfg.WGToolsDir, iptablesDir: cfg.IPTablesDir, ipForward: ipForwardPath}
	}
	return s
}

// LoadWireGuard returns the WireGuard servers stored in cfg.WireGuardDir and
// brings their interfaces up (direct mode). Nothing when WireGuard is off.
func LoadWireGuard(ctx context.Context, cfg config.Agent, log *slog.Logger) ([]*WGServer, error) {
	if cfg.WireGuard == "off" {
		return nil, nil
	}
	files, err := filepath.Glob(filepath.Join(cfg.WireGuardDir, "*.json"))
	if err != nil {
		return nil, err
	}
	var out []*WGServer
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var st wgState
		if err := json.Unmarshal(b, &st); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if st.ID+".json" != filepath.Base(f) || !validWGID(st.ID) {
			log.Warn("ignoring WireGuard state file with a wrong id", "file", f)
			continue
		}
		s := newWGServer(cfg, log, st)
		s.mu.Lock()
		s.applyLocked(ctx, false)
		s.mu.Unlock()
		out = append(out, s)
	}
	return out, nil
}

// wgIDPattern: the id is the interface name (IFNAMSIZ 16 with the NUL).
var wgIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,14}$`)

func validWGID(id string) bool { return wgIDPattern.MatchString(id) }

// wgPath is <dir>/<id><ext>; the id check keeps the path inside dir.
func wgPath(dir, id, ext string) string {
	if !wgIDPattern.MatchString(id) {
		panic("wireguard: invalid server id " + strconv.Quote(id)) // ids are checked on create and load
	}
	return filepath.Join(dir, id+ext)
}

// createWGServer validates req and returns the new server's state.
func createWGServer(req api.CreateServer) (wgState, error) {
	if !validWGID(req.ID) {
		return wgState{}, errors.New("id must be a network interface name: lower-case letters, digits and dashes, starting with a letter, at most 15 characters (e.g. wg0)")
	}
	st := wgState{ID: req.ID, Name: strings.TrimSpace(req.Name), ListenPort: req.Port,
		PublicHost: strings.TrimSpace(req.PublicHost), PublicPort: req.PublicPort, MTU: req.MTU, Keepalive: req.Keepalive}
	if st.Name == "" {
		st.Name = "WireGuard " + req.ID
	}
	if st.ListenPort == 0 {
		st.ListenPort = wgDefaultPort
	}
	if st.PublicPort == 0 {
		st.PublicPort = st.ListenPort
	}
	if st.ListenPort < 1 || st.ListenPort > 65535 || st.PublicPort < 1 || st.PublicPort > 65535 {
		return st, errors.New("port must be 1-65535")
	}
	if st.PublicHost == "" || strings.ContainsAny(st.PublicHost, " /\n") {
		return st, errors.New("public_host: the address clients connect to, e.g. vpn.example.com")
	}
	if st.MTU != 0 && (st.MTU < 1280 || st.MTU > 9000) {
		return st, errors.New("mtu must be 1280-9000")
	}
	switch {
	case st.Keepalive == 0:
		st.Keepalive = wgDefaultKeepalive
	case st.Keepalive < 0:
		st.Keepalive = 0
	case st.Keepalive > 600:
		return st, errors.New("keepalive must be at most 600 seconds")
	}
	subnet := req.Subnet
	if subnet == "" {
		subnet = wgDefaultSubnet
	}
	p, err := netip.ParsePrefix(subnet)
	if err != nil || !p.Addr().Is4() || p.Bits() > 30 || p.Bits() < 8 {
		return st, errors.New("subnet must be an IPv4 network from /8 to /30, e.g. 10.9.0.0/24")
	}
	p = p.Masked()
	st.Address = netip.PrefixFrom(p.Addr().Next(), p.Bits())
	for _, d := range req.DNS {
		d = strings.TrimSpace(d)
		if _, err := netip.ParseAddr(d); err != nil {
			return st, fmt.Errorf("dns: %q is not an IP address", d)
		}
		st.DNS = append(st.DNS, d)
	}
	allowed := req.AllowedIPs
	if len(allowed) == 0 {
		allowed = []string{"0.0.0.0/0", "::/0"}
	}
	for _, a := range allowed {
		p, err := netip.ParsePrefix(strings.TrimSpace(a))
		if err != nil {
			return st, fmt.Errorf("allowed_ips: %q is not a network", a)
		}
		st.AllowedIPs = append(st.AllowedIPs, p.Masked())
	}
	if st.PrivateKey, _, err = wg.GenerateKey(); err != nil {
		return st, err
	}
	st.Peers = []wgPeer{}
	return st, nil
}

// ID returns the server's id (its interface name).
func (s *WGServer) ID() string { return s.st.ID }

func (s *WGServer) subnet() netip.Prefix { return s.st.Address.Masked() }

func (s *WGServer) endpoint() string {
	return net.JoinHostPort(s.st.PublicHost, strconv.Itoa(s.st.PublicPort))
}

func (s *WGServer) confPath() string { return wgPath(s.dir, s.st.ID, ".conf") }

// Info describes the server.
func (s *WGServer) Info(ctx context.Context) api.ServerInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	pub, _ := wg.PublicKey(s.st.PrivateKey)
	info := api.ServerInfo{ID: s.st.ID, Type: api.TypeWireGuard, Name: s.st.Name, PublicKey: pub,
		ListenPort: s.st.ListenPort, Subnet: s.subnet().String(), Endpoint: s.endpoint(), Error: s.applyErr}
	if _, err := s.run.Dump(ctx, s.st.ID); err == nil {
		info.Running = true
	} else if info.Error == "" {
		info.Error = err.Error()
	}
	return info
}

// save writes the state and the interface configuration, then applies it.
// Called with mu held. A failed apply is reported in Info, not returned:
// the configuration on disk is right and is applied again at the next change.
func (s *WGServer) save(ctx context.Context) error {
	b, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(wgPath(s.dir, s.st.ID, ".json"), b); err != nil {
		return err
	}
	s.applyLocked(ctx, false)
	return nil
}

func (s *WGServer) serverConfig() string {
	srv := wg.Server{PrivateKey: s.st.PrivateKey, Address: []netip.Prefix{s.st.Address},
		ListenPort: s.st.ListenPort, MTU: s.st.MTU}
	var peers []wg.Peer
	for _, p := range s.st.Peers {
		if p.Disabled {
			continue
		}
		peers = append(peers, wg.Peer{Name: p.Name, PublicKey: p.PublicKey, PresharedKey: p.PSK,
			AllowedIPs: append([]netip.Prefix{netip.PrefixFrom(p.Address, 32)}, p.LAN...)})
	}
	return wg.ServerConfig(srv, peers)
}

func (s *WGServer) applyLocked(ctx context.Context, restart bool) {
	if err := writeFileAtomic(s.confPath(), []byte(s.serverConfig())); err != nil {
		s.applyErr = err.Error()
		return
	}
	restart = restart || (s.applied.IsValid() && s.applied != s.st.Address)
	if err := s.run.Apply(ctx, s.st.ID, s.confPath(), s.subnet(), s.applied.Masked(), restart); err != nil {
		s.applyErr = err.Error()
		s.log.Warn("WireGuard config not applied", "err", err)
		return
	}
	s.applied, s.applyErr = s.st.Address, ""
}

// remove takes the interface down and deletes its files.
func (s *WGServer) remove(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.run.Down(ctx, s.st.ID, s.confPath(), s.subnet()); err != nil {
		return err
	}
	for _, ext := range []string{".conf", ".json", ".dump", ".error", ".sync"} {
		if err := os.Remove(wgPath(s.dir, s.st.ID, ext)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func writeFileAtomic(path string, b []byte) error {
	tmp := path + ".td-tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *WGServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /info", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, s.Info(r.Context()))
	})
	mux.HandleFunc("GET /sessions", s.handleSessions)
	mux.HandleFunc("DELETE /sessions/{id}", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteError(w, http.StatusConflict, "WireGuard has no sessions to end; disable the client instead")
	})
	mux.HandleFunc("GET /peers", s.handleListPeers)
	mux.HandleFunc("POST /peers", s.handleAddPeer)
	mux.HandleFunc("GET /peers/{name}", s.handleGetPeer)
	mux.HandleFunc("PUT /peers/{name}", s.handleUpdatePeer)
	mux.HandleFunc("DELETE /peers/{name}", s.handleRemovePeer)
	mux.HandleFunc("GET /peers/{name}/conf", s.handlePeerConf)
	return mux
}

// live reads the interface and updates the session tracker. Called with mu held.
func (s *WGServer) live(ctx context.Context) (map[string]wg.DumpPeer, error) {
	out, err := s.run.Dump(ctx, s.st.ID)
	if err != nil {
		return nil, err
	}
	d, err := wg.ParseDump(out)
	if err != nil {
		return nil, err
	}
	now := s.now()
	byKey := make(map[string]wg.DumpPeer, len(d.Peers))
	for _, p := range d.Peers {
		byKey[p.PublicKey] = p
		last, known := s.last[p.PublicKey]
		cur := s.sess[p.PublicKey]
		switch {
		case p.Connected(now) && cur == nil:
			// Counters run for the interface's lifetime: a session's traffic
			// is what was added since it started. Unknown before (agent just
			// started): count from now.
			base := [2]int64{p.RxBytes, p.TxBytes}
			if known && last[0] <= p.RxBytes && last[1] <= p.TxBytes {
				base = last
			}
			s.sess[p.PublicKey] = &wgSession{since: p.LastHandshake, baseRx: base[0], baseTx: base[1]}
		case !p.Connected(now) && cur != nil:
			delete(s.sess, p.PublicKey)
		case cur != nil && (p.RxBytes < cur.baseRx || p.TxBytes < cur.baseTx):
			cur.baseRx, cur.baseTx = 0, 0 // interface restarted
		}
		s.last[p.PublicKey] = [2]int64{p.RxBytes, p.TxBytes}
	}
	for k := range s.sess {
		if _, ok := byKey[k]; !ok {
			delete(s.sess, k)
		}
	}
	return byKey, nil
}

// sessionID is a stable number for a peer (history keys sessions by it).
func sessionID(pub string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(pub))
	return int64(h.Sum64() >> 1)
}

func (s *WGServer) handleSessions(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	live, err := s.live(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	out := api.Sessions{Time: s.now().UTC(), Sessions: []api.Session{}}
	for _, p := range s.st.Peers {
		d, ok := live[p.PublicKey]
		out.BytesIn += d.RxBytes
		out.BytesOut += d.TxBytes
		cur := s.sess[p.PublicKey]
		if !ok || cur == nil {
			continue
		}
		out.Sessions = append(out.Sessions, api.Session{ClientID: sessionID(p.PublicKey), Name: p.Name,
			RealAddress: d.Endpoint, VirtualAddress: p.Address.String(),
			BytesReceived: d.RxBytes - cur.baseRx, BytesSent: d.TxBytes - cur.baseTx, ConnectedSince: cur.since})
	}
	out.Clients = int64(len(out.Sessions))
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *WGServer) peerView(p wgPeer, live map[string]wg.DumpPeer) api.Peer {
	v := api.Peer{Name: p.Name, PublicKey: p.PublicKey, Address: p.Address.String(), Disabled: p.Disabled,
		HasPSK: p.PSK != "", CreatedAt: p.CreatedAt, LAN: []string{}}
	for _, l := range p.LAN {
		v.LAN = append(v.LAN, l.String())
	}
	if d, ok := live[p.PublicKey]; ok {
		v.Connected = d.Connected(s.now())
		v.Endpoint, v.LastHandshake = d.Endpoint, d.LastHandshake
		v.BytesReceived, v.BytesSent = d.RxBytes, d.TxBytes
	}
	return v
}

func (s *WGServer) handleListPeers(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	live, _ := s.live(r.Context()) // the list works without the interface
	out := make([]api.Peer, 0, len(s.st.Peers))
	for _, p := range s.st.Peers {
		out = append(out, s.peerView(p, live))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *WGServer) find(name string) int {
	return slices.IndexFunc(s.st.Peers, func(p wgPeer) bool { return p.Name == name })
}

// peer resolves {name}; with mu held.
func (s *WGServer) peer(w http.ResponseWriter, r *http.Request) (int, bool) {
	name := r.PathValue("name")
	if !pki.ValidName(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid client name")
		return -1, false
	}
	i := s.find(name)
	if i < 0 {
		httpx.WriteError(w, http.StatusNotFound, "no client "+name)
		return -1, false
	}
	return i, true
}

func (s *WGServer) handleGetPeer(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.peer(w, r)
	if !ok {
		return
	}
	live, _ := s.live(r.Context())
	httpx.WriteJSON(w, http.StatusOK, s.peerView(s.st.Peers[i], live))
}

// nextAddress returns the first free address of the subnet.
func (s *WGServer) nextAddress() (netip.Addr, bool) {
	sn := s.subnet()
	used := map[netip.Addr]bool{s.st.Address.Addr(): true}
	for _, p := range s.st.Peers {
		used[p.Address] = true
	}
	for a := sn.Addr().Next(); sn.Contains(a); a = a.Next() {
		if !used[a] && sn.Contains(a.Next()) { // skip the broadcast address
			return a, true
		}
	}
	return netip.Addr{}, false
}

func (s *WGServer) checkAddress(a netip.Addr, self int) error {
	sn := s.subnet()
	if !sn.Contains(a) || a == sn.Addr() || !sn.Contains(a.Next()) {
		return fmt.Errorf("%s is not a usable address in %s", a, sn)
	}
	if a == s.st.Address.Addr() {
		return fmt.Errorf("%s is the server's address", a)
	}
	for i, p := range s.st.Peers {
		if i != self && p.Address == a {
			return fmt.Errorf("%s is used by %s", a, p.Name)
		}
	}
	return nil
}

func parseLAN(in []string) ([]netip.Prefix, error) {
	out := []netip.Prefix{}
	for _, l := range in {
		p, err := netip.ParsePrefix(strings.TrimSpace(l))
		if err != nil {
			return nil, fmt.Errorf("lan: %q is not a network (e.g. 192.168.5.0/24)", l)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

func (s *WGServer) handleAddPeer(w http.ResponseWriter, r *http.Request) {
	var req api.NewPeer
	if !decodeBody(w, r, &req) {
		return
	}
	if !pki.ValidName(req.Name) {
		httpx.WriteError(w, http.StatusBadRequest, "name: letters, digits, dot, dash and underscore")
		return
	}
	lan, err := parseLAN(req.LAN)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.find(req.Name) >= 0 {
		httpx.WriteError(w, http.StatusConflict, "a client named "+req.Name+" exists")
		return
	}
	var addr netip.Addr
	if req.Address != "" {
		if addr, err = netip.ParseAddr(req.Address); err == nil {
			err = s.checkAddress(addr, -1)
		}
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "address: "+err.Error())
			return
		}
	} else if a, ok := s.nextAddress(); ok {
		addr = a
	} else {
		httpx.WriteError(w, http.StatusConflict, "no free address left in "+s.subnet().String())
		return
	}
	p := wgPeer{Name: req.Name, Address: addr, LAN: lan, CreatedAt: s.now().UTC()}
	if p.PrivateKey, p.PublicKey, err = wg.GenerateKey(); err == nil && !req.NoPSK {
		p.PSK, err = wg.GeneratePSK()
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.st.Peers = append(s.st.Peers, p)
	if err := s.save(r.Context()); err != nil {
		s.st.Peers = s.st.Peers[:len(s.st.Peers)-1]
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.last[p.PublicKey] = [2]int64{} // a new peer's counters start at zero
	s.log.Info("WireGuard client added", "name", p.Name, "address", p.Address)
	httpx.WriteJSON(w, http.StatusCreated, s.peerView(p, nil))
}

func (s *WGServer) handleUpdatePeer(w http.ResponseWriter, r *http.Request) {
	var req api.PeerUpdate
	if !decodeBody(w, r, &req) {
		return
	}
	var lan []netip.Prefix
	if req.LAN != nil {
		var err error
		if lan, err = parseLAN(req.LAN); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.peer(w, r)
	if !ok {
		return
	}
	old := s.st.Peers[i]
	p := &s.st.Peers[i]
	if req.Disabled != nil {
		p.Disabled = *req.Disabled
	}
	if req.LAN != nil {
		p.LAN = lan
	}
	if err := s.save(r.Context()); err != nil {
		s.st.Peers[i] = old
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if old.Disabled != p.Disabled {
		// Disabling takes the peer off the interface; enabled again, its
		// counters start at zero.
		delete(s.sess, p.PublicKey)
		if p.Disabled {
			delete(s.last, p.PublicKey)
		} else {
			s.last[p.PublicKey] = [2]int64{}
		}
	}
	s.log.Info("WireGuard client updated", "name", p.Name, "disabled", p.Disabled, "lan", len(p.LAN))
	live, _ := s.live(r.Context())
	httpx.WriteJSON(w, http.StatusOK, s.peerView(*p, live))
}

func (s *WGServer) handleRemovePeer(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.peer(w, r)
	if !ok {
		return
	}
	old := slices.Clone(s.st.Peers)
	name := s.st.Peers[i].Name
	s.st.Peers = slices.Delete(s.st.Peers, i, i+1)
	if err := s.save(r.Context()); err != nil {
		s.st.Peers = old
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.Info("WireGuard client removed", "name", name)
	w.WriteHeader(http.StatusNoContent)
}

func (s *WGServer) handlePeerConf(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.peer(w, r)
	if !ok {
		return
	}
	p := s.st.Peers[i]
	if p.Disabled {
		httpx.WriteError(w, http.StatusConflict, p.Name+" is disabled")
		return
	}
	pub, err := wg.PublicKey(s.st.PrivateKey)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	conf := wg.ClientConfig(wg.Client{PrivateKey: p.PrivateKey, Address: []netip.Prefix{netip.PrefixFrom(p.Address, 32)},
		DNS: s.st.DNS, MTU: s.st.MTU, ServerKey: pub, PresharedKey: p.PSK, Endpoint: s.endpoint(),
		AllowedIPs: s.st.AllowedIPs, Keepalive: s.st.Keepalive})
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+p.Name+`.conf"`)
	_, _ = w.Write([]byte(conf))
}
