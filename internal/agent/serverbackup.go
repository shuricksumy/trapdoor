package agent

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/backup"
	"github.com/shuricksumy/trapdoor/internal/httpx"
	"github.com/shuricksumy/trapdoor/internal/pki"
)

// One server's backup (GET /v1/servers/{id}/backup) is a tar:
//
//	server.json   ServerBackup: what it is
//	state.json    WireGuard / AmneziaWG: the server's state (keys, clients, rules)
//	files/…       OpenVPN: the server's directory
//	pki/…, ccd/…  OpenVPN: the PKI and ccd when they are outside it
//
// POST /v1/servers/restore takes the same tar and brings the server back.

// serverBackupFormat is the version of server.json.
const serverBackupFormat = 1

// ServerBackup is server.json in a server's backup.
type ServerBackup struct {
	Format int    `json:"format"`
	Type   string `json:"type"`
	ID     string `json:"id"`
	Name   string `json:"name"`
	// First: the OpenVPN server from the TD_* settings (restored into
	// TD_OPENVPN_DIR); otherwise an added one (TD_OPENVPN_SERVERS_DIR/<id>).
	First bool `json:"first,omitempty"`
}

func writeJSONEntry(tw *tar.Writer, name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(b)), ModTime: time.Now(), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err = tw.Write(b)
	return err
}

// hostFiles in the first server's directory belong to the agent, not to
// the server: its TLS identity.
var hostFiles = []string{"trapdoor-agent.crt", "trapdoor-agent.key"}

func (s *Server) handleBackup(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/x-tar")
	tw := tar.NewWriter(w)
	c := s.cfg
	err := writeJSONEntry(tw, "server.json", ServerBackup{Format: serverBackupFormat, Type: api.TypeOpenVPN, ID: s.id, Name: s.name, First: s.extra == nil})
	if err == nil {
		exclude := append([]string{}, hostFiles...)
		if s.extra == nil && c.OpenVPNServersDir != "" && inside(c.OpenVPNServersDir, c.OpenVPNDir) {
			rel, _ := filepath.Rel(c.OpenVPNDir, c.OpenVPNServersDir)
			exclude = append(exclude, rel) // the other servers
		}
		err = backup.AddDir(tw, c.OpenVPNDir, "files", exclude...)
	}
	for _, x := range []struct{ prefix, dir string }{{"pki", c.PKIDir}, {"ccd", c.CCDDir}} {
		if err == nil && x.dir != "" && !inside(x.dir, c.OpenVPNDir) {
			err = backup.AddDir(tw, x.dir, x.prefix)
		}
	}
	if err == nil {
		err = tw.Close()
	}
	if err != nil {
		s.log.Error("server backup failed", "err", err) // the cut tar tells the web side
		return
	}
	s.log.Info("server backup sent")
}

func (s *WGServer) handleBackup(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	st := s.st
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/x-tar")
	tw := tar.NewWriter(w)
	err := writeJSONEntry(tw, "server.json", ServerBackup{Format: serverBackupFormat, Type: st.Type, ID: st.ID, Name: st.Name})
	if err == nil {
		err = writeJSONEntry(tw, "state.json", st)
	}
	if err == nil {
		err = tw.Close()
	}
	if err != nil {
		s.log.Error("server backup failed", "err", err)
		return
	}
	s.log.Info("server backup sent")
}

// handleRestoreServer brings a server back from its backup (the request
// body). ?replace=1 removes a server with the same id first (an added
// OpenVPN server's old files go to .removed/); without it that is a 409.
func (h *Host) handleRestoreServer(w http.ResponseWriter, r *http.Request) {
	replace := r.URL.Query().Get("replace") == "1"
	tr := tar.NewReader(http.MaxBytesReader(w, r.Body, 4<<30))
	hd, err := tr.Next()
	if err != nil || hd.Name != "server.json" {
		httpx.WriteError(w, http.StatusBadRequest, "not a server backup (no server.json)")
		return
	}
	var sb ServerBackup
	if err := json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(&sb); err != nil || sb.Format != serverBackupFormat {
		httpx.WriteError(w, http.StatusBadRequest, "unknown server backup format")
		return
	}
	var info api.ServerInfo
	var status int
	switch sb.Type {
	case api.TypeWireGuard, api.TypeAmneziaWG:
		info, status, err = h.restoreWG(r, tr, sb, replace)
	case api.TypeOpenVPN:
		if sb.First || sb.ID == DefaultServerID {
			info, status, err = h.restoreFirstOpenVPN(r, tr, replace)
		} else {
			info, status, err = h.restoreOpenVPN(r, tr, sb, replace)
		}
	default:
		status, err = http.StatusBadRequest, fmt.Errorf("unknown server type %q", sb.Type)
	}
	if err != nil {
		httpx.WriteError(w, status, err.Error())
		return
	}
	h.log.Info("server restored", "server", info.ID, "type", sb.Type, "replace", replace)
	httpx.WriteJSON(w, http.StatusCreated, info)
}

// removeExisting takes the server with the id away before a restore that
// replaces it.
func (h *Host) removeExisting(r *http.Request, id string, replace bool) (int, error) {
	m, ok := h.get(id)
	if !ok {
		return 0, nil
	}
	if !replace {
		return http.StatusConflict, fmt.Errorf("a server with id %s exists (restore with replace to swap it)", id)
	}
	var err error
	switch s := m.b.(type) {
	case *WGServer:
		err = s.remove(r.Context())
	case *Server:
		if s.extra == nil {
			return http.StatusConflict, errors.New("the first OpenVPN server is restored in place")
		}
		_, err = s.remove(r.Context())
	}
	if err != nil {
		return http.StatusBadGateway, err
	}
	h.unmount(id)
	return 0, nil
}

func (h *Host) unmount(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, x := range h.servers {
		if x.b.ID() == id {
			h.servers = append(h.servers[:i], h.servers[i+1:]...)
			return
		}
	}
}

func (h *Host) restoreWG(r *http.Request, tr *tar.Reader, sb ServerBackup, replace bool) (api.ServerInfo, int, error) {
	h.mu.RLock()
	cfg := h.cfg
	h.mu.RUnlock()
	if cfg.WireGuard == "" || cfg.WireGuard == "off" {
		return api.ServerInfo{}, http.StatusBadRequest, errors.New("WireGuard is turned off on this agent (TD_WIREGUARD=off)")
	}
	hd, err := tr.Next()
	if err != nil || hd.Name != "state.json" {
		return api.ServerInfo{}, http.StatusBadRequest, errors.New("the backup has no state.json")
	}
	var st wgState
	if err := json.NewDecoder(io.LimitReader(tr, 64<<20)).Decode(&st); err != nil {
		return api.ServerInfo{}, http.StatusBadRequest, fmt.Errorf("state.json: %w", err)
	}
	if st.ID != sb.ID || !validWGID(st.ID) || st.Type != sb.Type || !st.Address.IsValid() || st.PrivateKey == "" {
		return api.ServerInfo{}, http.StatusBadRequest, errors.New("state.json does not describe the server in server.json")
	}
	if status, err := h.removeExisting(r, st.ID, replace); err != nil {
		return api.ServerInfo{}, status, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s, status, err := h.addWGLocked(r.Context(), cfg, st)
	if err != nil {
		return api.ServerInfo{}, status, err
	}
	return s.Info(r.Context()), 0, nil
}

func (h *Host) restoreOpenVPN(r *http.Request, tr *tar.Reader, sb ServerBackup, replace bool) (api.ServerInfo, int, error) {
	h.mu.RLock()
	base := h.ovpn
	h.mu.RUnlock()
	if base == nil || base.OpenVPNServersDir == "" {
		return api.ServerInfo{}, http.StatusBadRequest, errors.New("this agent cannot add OpenVPN servers (TD_OPENVPN_SERVERS_DIR)")
	}
	if !validOVPNID(sb.ID) {
		return api.ServerInfo{}, http.StatusBadRequest, fmt.Errorf("invalid server id %q", sb.ID)
	}
	if status, err := h.removeExisting(r, sb.ID, replace); err != nil {
		return api.ServerInfo{}, status, err
	}
	// Unpack next to the servers first; it becomes the server only when
	// everything is there and fits.
	if err := os.MkdirAll(base.OpenVPNServersDir, 0o755); err != nil {
		return api.ServerInfo{}, http.StatusInternalServerError, err
	}
	stage, err := os.MkdirTemp(base.OpenVPNServersDir, ".restore-*")
	if err != nil {
		return api.ServerInfo{}, http.StatusInternalServerError, err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if _, err := backup.ExtractFrom(tr, []backup.Target{{Prefix: "files", Dir: stage}}, false); err != nil {
		return api.ServerInfo{}, http.StatusBadRequest, err
	}
	data, err := os.ReadFile(filepath.Join(stage, ovpnStateFile))
	if err != nil {
		return api.ServerInfo{}, http.StatusBadRequest, errors.New("the backup has no " + ovpnStateFile)
	}
	var st ovpnState
	if err := json.Unmarshal(data, &st); err != nil || st.ID != sb.ID {
		return api.ServerInfo{}, http.StatusBadRequest, errors.New(ovpnStateFile + " does not describe the server in server.json")
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.getLocked(st.ID); ok {
		return api.ServerInfo{}, http.StatusConflict, errors.New("a server with id " + st.ID + " exists")
	}
	if name := h.portUsedLocked(st.Port, st.Proto); name != "" {
		return api.ServerInfo{}, http.StatusConflict, fmt.Errorf("%s port %d is used by %s", strings.ToUpper(st.Proto), st.Port, name)
	}
	// The management port may be taken on this machine: use a free one.
	used := map[int]bool{}
	for _, m := range h.servers {
		if s, ok := m.b.(*Server); ok {
			used[s.mgmtPort()] = true
		}
	}
	host, p, _ := net.SplitHostPort(st.Mgmt)
	port, _ := strconv.Atoi(p)
	if host == "" || port == 0 || used[port] {
		if port == 0 {
			port = 2081
		}
		for used[port] {
			port++
		}
		if host == "" {
			host = "127.0.0.1"
		}
		st.Mgmt = net.JoinHostPort(host, strconv.Itoa(port))
		if err := setManagementPort(filepath.Join(stage, "server.conf"), host, port); err != nil {
			return api.ServerInfo{}, http.StatusInternalServerError, err
		}
		data, _ := json.MarshalIndent(st, "", "  ")
		if err := writeAtomic(filepath.Join(stage, ovpnStateFile), append(data, '\n'), 0o600); err != nil {
			return api.ServerInfo{}, http.StatusInternalServerError, err
		}
	}
	dir := filepath.Join(base.OpenVPNServersDir, st.ID)
	if _, err := os.Stat(dir); err == nil {
		return api.ServerInfo{}, http.StatusConflict, errors.New(dir + " exists; move it away first")
	}
	if err := os.Rename(stage, dir); err != nil {
		return api.ServerInfo{}, http.StatusInternalServerError, err
	}
	c := ovpnConfig(*base, st)
	var p2 pki.PKI
	if store, err := pki.Open(c.PKIDir, pki.Options{CAPassphrase: c.CAPassphrase, CRLCopies: c.CRLCopies}); err == nil {
		p2 = store
	}
	s := newOpenVPNServer(*base, h.log, st, p2)
	h.mountLocked(s)
	go s.ApplyFirewallAtStart(context.Background()) // outlives the request
	return s.Info(r.Context()), 0, nil
}

// setManagementPort rewrites the "management" line of a server.conf.
func setManagementPort(conf, host string, port int) error {
	data, err := os.ReadFile(conf)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	for i, l := range lines {
		f := strings.Fields(l)
		if len(f) >= 3 && f[0] == "management" {
			lines[i] = strings.Join(append([]string{"management", host, strconv.Itoa(port)}, f[3:]...), " ")
		}
	}
	return writeAtomic(conf, []byte(strings.Join(lines, "\n")), 0o644)
}

// restoreFirstOpenVPN puts the first server's files back into its
// directories: on a machine where it is not set up, or with replace (its
// files are overwritten, others stay) and then restarted.
func (h *Host) restoreFirstOpenVPN(r *http.Request, tr *tar.Reader, replace bool) (api.ServerInfo, int, error) {
	m, ok := h.get(DefaultServerID)
	if !ok {
		return api.ServerInfo{}, http.StatusBadRequest, errors.New("this agent has no OpenVPN server")
	}
	s := m.b.(*Server)
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	if _, err := os.Stat(s.conf.Path); err == nil && !replace {
		return api.ServerInfo{}, http.StatusConflict, errors.New("the OpenVPN server is set up already (restore with replace to overwrite it)")
	}
	c := s.cfg
	targets := []backup.Target{{Prefix: "files", Dir: c.OpenVPNDir}, {Prefix: "pki", Dir: c.PKIDir}, {Prefix: "ccd", Dir: c.CCDDir}}
	if _, err := backup.ExtractFrom(tr, targets, true); err != nil {
		return api.ServerInfo{}, http.StatusBadRequest, err
	}
	store, err := pki.Open(c.PKIDir, pki.Options{CAPassphrase: c.CAPassphrase, CRLCopies: c.CRLCopies})
	if err != nil {
		return api.ServerInfo{}, http.StatusInternalServerError, fmt.Errorf("the restored PKI does not open: %w", err)
	}
	s.setPKI(store)
	// OpenVPN reads the new files when it starts again (a supervisor
	// starts it once server.conf is there).
	sctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	sig := s.cfg.RestartSignal
	if sig == "" {
		sig = "SIGHUP"
	}
	if err := s.mgmt.Signal(sctx, sig); err != nil {
		s.log.Info("restore: OpenVPN not running yet", "err", err)
	}
	cancel()
	go s.ApplyFirewallAtStart(context.Background()) // outlives the request
	return s.Info(r.Context()), 0, nil
}
