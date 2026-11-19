package agent

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/httpx"
	"github.com/shuricksumy/trapdoor/internal/pki"
	"github.com/shuricksumy/trapdoor/internal/wg"
)

// A WireGuard client's config takes the server's client defaults (DNS,
// AllowedIPs, MTU, keepalive) unless the client has its own.

// peerSettings is a checked api.PeerSettings.
type peerSettings struct {
	notes     string
	dns       []string
	allowed   []netip.Prefix
	mtu       int
	keepalive *int
}

func parseDNS(in []string) ([]string, error) {
	var out []string
	for _, d := range in {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if _, err := netip.ParseAddr(d); err != nil {
			return nil, fmt.Errorf("dns: %q is not an IP address", d)
		}
		out = append(out, d)
	}
	return out, nil
}

func parseAllowed(in []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, a := range in {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		p, err := netip.ParsePrefix(a)
		if err != nil {
			return nil, fmt.Errorf("allowed_ips: %q is not a network", a)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

func checkMTU(m int) error {
	if m != 0 && (m < 1280 || m > 9000) {
		return errors.New("mtu must be 1280-9000")
	}
	return nil
}

func checkKeepalive(k int) error {
	if k < 0 || k > 600 {
		return errors.New("keepalive must be 0-600 seconds (0 = off)")
	}
	return nil
}

func parsePeerSettings(in api.PeerSettings) (peerSettings, error) {
	out := peerSettings{notes: strings.TrimSpace(in.Notes), mtu: in.MTU, keepalive: in.Keepalive}
	if len(out.notes) > 500 || strings.ContainsAny(out.notes, "\r\n") {
		return out, errors.New("notes: one line, at most 500 characters")
	}
	var err error
	if out.dns, err = parseDNS(in.DNS); err != nil {
		return out, err
	}
	if out.allowed, err = parseAllowed(in.AllowedIPs); err != nil {
		return out, err
	}
	if err := checkMTU(out.mtu); err != nil {
		return out, err
	}
	if out.keepalive != nil {
		if err := checkKeepalive(*out.keepalive); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (set peerSettings) apply(p *wgPeer) {
	p.Notes, p.DNS, p.AllowedIPs, p.MTU, p.Keepalive = set.notes, set.dns, set.allowed, set.mtu, set.keepalive
}

// wantPSK decides whether a new client gets a preshared key.
func (s *WGServer) wantPSK(psk *bool, noPSK bool) bool {
	switch {
	case psk != nil:
		return *psk
	case noPSK:
		return false
	}
	return !s.st.NoPSKDefault
}

func (s *WGServer) findKey(pub string) int {
	for i, p := range s.st.Peers {
		if p.PublicKey == pub {
			return i
		}
	}
	return -1
}

// ownKeyPlaceholder stands in a config for a private key trapdoor never had.
const ownKeyPlaceholder = "(the private key of this device: put it here)"

// clientConfig is p's config: its own settings, else the server's.
func (s *WGServer) clientConfig(p wgPeer, serverKey string) wg.Client {
	c := wg.Client{PrivateKey: p.PrivateKey, Address: []netip.Prefix{netip.PrefixFrom(p.Address, 32)},
		DNS: s.st.DNS, MTU: s.mtu(), ServerKey: serverKey, PresharedKey: p.PSK, Endpoint: s.endpoint(),
		AllowedIPs: s.st.AllowedIPs, Keepalive: s.st.Keepalive}
	if c.PrivateKey == "" {
		c.PrivateKey = ownKeyPlaceholder
	}
	if len(p.DNS) > 0 {
		c.DNS = p.DNS
	}
	if len(p.AllowedIPs) > 0 {
		c.AllowedIPs = p.AllowedIPs
	}
	if p.MTU > 0 {
		c.MTU = p.MTU
	}
	if p.Keepalive != nil {
		c.Keepalive = *p.Keepalive
	}
	if o := s.st.Obfuscation; o != nil {
		c.Extra = wg.ObfuscationSettings(*o, true)
	}
	return c
}

func (s *WGServer) settings() api.WGSettings {
	out := api.WGSettings{Name: s.st.Name, PublicHost: s.st.PublicHost, PublicPort: s.st.PublicPort,
		DNS: []string{}, AllowedIPs: []string{}, MTU: s.st.MTU, Keepalive: s.st.Keepalive, PSK: !s.st.NoPSKDefault}
	out.DNS = append(out.DNS, s.st.DNS...)
	for _, a := range s.st.AllowedIPs {
		out.AllowedIPs = append(out.AllowedIPs, a.String())
	}
	return out
}

func (s *WGServer) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	httpx.WriteJSON(w, http.StatusOK, s.settings())
}

// handleSetSettings changes the server's name, endpoint and client
// defaults. Every client's config changes with them (devices need the new
// one); the server's interface is not touched.
func (s *WGServer) handleSetSettings(w http.ResponseWriter, r *http.Request) {
	var req api.WGSettings
	if !decodeBody(w, r, &req) {
		return
	}
	req.Name, req.PublicHost = strings.TrimSpace(req.Name), strings.TrimSpace(req.PublicHost)
	bad := func(msg string) { httpx.WriteError(w, http.StatusBadRequest, msg) }
	if req.Name == "" || len(req.Name) > 64 {
		bad("name: 1-64 characters")
		return
	}
	if req.PublicHost == "" || strings.ContainsAny(req.PublicHost, " /\n") {
		bad("public_host: the address clients connect to, e.g. vpn.example.com")
		return
	}
	if req.PublicPort < 1 || req.PublicPort > 65535 {
		bad("public_port must be 1-65535")
		return
	}
	dns, err := parseDNS(req.DNS)
	if err != nil {
		bad(err.Error())
		return
	}
	allowed, err := parseAllowed(req.AllowedIPs)
	if err != nil {
		bad(err.Error())
		return
	}
	if len(allowed) == 0 {
		bad("allowed_ips: at least one network (0.0.0.0/0, ::/0 sends everything)")
		return
	}
	if err := checkMTU(req.MTU); err != nil {
		bad(err.Error())
		return
	}
	if err := checkKeepalive(req.Keepalive); err != nil {
		bad(err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.st
	s.st.Name, s.st.PublicHost, s.st.PublicPort = req.Name, req.PublicHost, req.PublicPort
	s.st.DNS, s.st.AllowedIPs, s.st.MTU, s.st.Keepalive, s.st.NoPSKDefault = dns, allowed, req.MTU, req.Keepalive, !req.PSK
	if err := s.save(r.Context()); err != nil {
		s.st = old
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.Info("WireGuard settings changed", "endpoint", s.endpoint(), "dns", len(dns), "mtu", req.MTU, "keepalive", req.Keepalive)
	httpx.WriteJSON(w, http.StatusOK, s.settings())
}

// handleAddPeers adds several clients at once: <prefix>-N on the next free
// numbers and addresses, with generated keys.
func (s *WGServer) handleAddPeers(w http.ResponseWriter, r *http.Request) {
	var req api.NewPeers
	if !decodeBody(w, r, &req) {
		return
	}
	req.Prefix = strings.TrimSpace(req.Prefix)
	if !pki.ValidName(req.Prefix+"-1") || len(req.Prefix) > 40 {
		httpx.WriteError(w, http.StatusBadRequest, "prefix: letters, digits, dot, dash and underscore")
		return
	}
	if req.Count < 1 || req.Count > 250 {
		httpx.WriteError(w, http.StatusBadRequest, "count must be 1-250")
		return
	}
	set, err := parsePeerSettings(req.PeerSettings)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	before := len(s.st.Peers)
	var added []wgPeer
	n := 1
	for len(added) < req.Count {
		name := req.Prefix + "-" + strconv.Itoa(n)
		n++
		if s.find(name) >= 0 {
			continue
		}
		addr, ok := s.nextAddress()
		if !ok {
			s.st.Peers = s.st.Peers[:before]
			httpx.WriteError(w, http.StatusConflict, fmt.Sprintf("only %d free addresses left in %s", len(added), s.subnet()))
			return
		}
		p := wgPeer{Name: name, Address: addr, CreatedAt: s.now().UTC()}
		set.apply(&p)
		var err error
		if p.PrivateKey, p.PublicKey, err = wg.GenerateKey(); err == nil && s.wantPSK(req.PSK, false) {
			p.PSK, err = wg.GeneratePSK()
		}
		if err != nil {
			s.st.Peers = s.st.Peers[:before]
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.st.Peers = append(s.st.Peers, p) // nextAddress sees it
		added = append(added, p)
	}
	if err := s.save(r.Context()); err != nil {
		s.st.Peers = s.st.Peers[:before]
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]api.Peer, 0, len(added))
	for _, p := range added {
		s.last[p.PublicKey] = [2]int64{}
		out = append(out, s.peerView(p, nil))
	}
	s.log.Info("WireGuard clients added", "count", len(added), "prefix", req.Prefix)
	httpx.WriteJSON(w, http.StatusCreated, out)
}
