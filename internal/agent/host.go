package agent

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/config"
	"github.com/shuricksumy/trapdoor/internal/httpx"
	"github.com/shuricksumy/trapdoor/internal/version"
)

// backend is one VPN server (OpenVPN *Server or WireGuard *WGServer).
type backend interface {
	ID() string
	Info(ctx context.Context) api.ServerInfo
	routes() http.Handler
}

type mounted struct {
	b backend
	h http.Handler
}

// Host is the agent HTTP API: the VPN servers on this machine, each under
// /v1/servers/{id}/. WireGuard servers can be added and removed at runtime.
type Host struct {
	token string
	log   *slog.Logger
	cfg   config.Agent  // WireGuard settings for new servers
	ovpn  *config.Agent // settings for new OpenVPN servers (nil: none can be added)

	mu      sync.RWMutex
	servers []mounted
}

// NewHost returns the API for servers; token is the shared secret the web
// process sends.
func NewHost(token string, log *slog.Logger, servers ...*Server) *Host {
	h := &Host{token: token, log: log, cfg: config.Agent{WireGuard: "off"}}
	for _, s := range servers {
		h.add(s)
	}
	return h
}

// EnableWireGuard lets the API create WireGuard servers (cfg.WireGuard,
// cfg.WireGuardDir) and mounts the existing ones.
func (h *Host) EnableWireGuard(cfg config.Agent, existing []*WGServer) {
	h.mu.Lock()
	h.cfg = cfg
	h.mu.Unlock()
	for _, s := range existing {
		h.add(s)
	}
}

// EnableOpenVPN lets the API add OpenVPN servers (in
// cfg.OpenVPNServersDir) and mounts the existing ones.
func (h *Host) EnableOpenVPN(cfg config.Agent, existing []*Server) {
	h.mu.Lock()
	h.ovpn = &cfg
	h.mu.Unlock()
	for _, s := range existing {
		h.add(s)
	}
}

func (h *Host) add(b backend) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.mountLocked(b)
}

func (h *Host) mountLocked(b backend) {
	if s, ok := b.(*Server); ok {
		s.others = h.subnets
	}
	h.servers = append(h.servers, mounted{b: b, h: http.StripPrefix("/v1/servers/"+b.ID(), b.routes())})
}

// subnets are the VPN subnets of every server except self.
func (h *Host) subnets(self string) []netip.Prefix {
	var out []netip.Prefix
	for _, b := range h.list() {
		if b.ID() == self {
			continue
		}
		var p netip.Prefix
		switch s := b.(type) {
		case *Server:
			p = s.subnet()
		case *WGServer:
			p = s.subnet()
		}
		if p.IsValid() {
			out = append(out, p)
		}
	}
	return out
}

// portUsedLocked names the server already listening on port/proto.
func (h *Host) portUsedLocked(port int, proto string) string {
	for _, m := range h.servers {
		switch s := m.b.(type) {
		case *Server:
			if p, pr, ok := s.listen(); ok && p == port && pr == proto {
				return s.name
			}
		case *WGServer:
			if proto == "udp" && s.st.ListenPort == port {
				return s.st.Name
			}
		}
	}
	return ""
}

func (h *Host) get(id string) (mounted, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, m := range h.servers {
		if m.b.ID() == id {
			return m, true
		}
	}
	return mounted{}, false
}

func (h *Host) list() []backend {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]backend, len(h.servers))
	for i, m := range h.servers {
		out[i] = m.b
	}
	return out
}

// Handler returns the agent's HTTP handler.
//
// /healthz is unauthenticated (for container health checks);
// everything under /v1/ requires the shared token.
func (h *Host) Handler() http.Handler {
	v1 := http.NewServeMux()
	v1.HandleFunc("GET /v1/info", h.handleInfo)
	v1.HandleFunc("GET /v1/backup", h.handleBackup)
	v1.HandleFunc("POST /v1/servers", h.handleCreate)
	v1.HandleFunc("DELETE /v1/servers/{server}", h.handleRemove)
	v1.HandleFunc("/v1/servers/{server}/", func(w http.ResponseWriter, r *http.Request) {
		m, ok := h.get(r.PathValue("server"))
		if !ok {
			httpx.WriteError(w, http.StatusNotFound, "no server "+r.PathValue("server"))
			return
		}
		m.h.ServeHTTP(w, r)
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, api.Health{Status: "ok"})
	})
	mux.Handle("/v1/", h.requireToken(v1))

	return httpx.LogRequests(h.log, mux)
}

func (h *Host) handleInfo(w http.ResponseWriter, r *http.Request) {
	host, _ := os.Hostname()
	info := api.AgentInfo{Version: version.Version, Hostname: host, Servers: []api.ServerInfo{}}
	for _, s := range h.list() {
		info.Servers = append(info.Servers, s.Info(r.Context()))
	}
	httpx.WriteJSON(w, http.StatusOK, info)
}

func (h *Host) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req api.CreateServer
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Type == api.TypeOpenVPN {
		h.createOpenVPN(w, r, req)
		return
	}
	if req.Type != api.TypeWireGuard && req.Type != api.TypeAmneziaWG {
		httpx.WriteError(w, http.StatusBadRequest, "type must be "+api.TypeOpenVPN+", "+api.TypeWireGuard+" or "+api.TypeAmneziaWG)
		return
	}
	h.mu.RLock()
	cfg := h.cfg
	h.mu.RUnlock()
	if cfg.WireGuard == "off" {
		httpx.WriteError(w, http.StatusBadRequest, "WireGuard is turned off on this agent (TD_WIREGUARD=off)")
		return
	}
	st, err := createWGServer(req)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	// One lock for the checks and the add, so two creates cannot both pass.
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, m := range h.servers {
		if m.b.ID() == st.ID {
			httpx.WriteError(w, http.StatusConflict, "a server with id "+st.ID+" exists")
			return
		}
		if o, ok := m.b.(*Server); ok {
			if sub := o.subnet(); sub.IsValid() && sub.Overlaps(st.Address.Masked()) {
				httpx.WriteError(w, http.StatusConflict, "subnet overlaps "+o.name+" ("+sub.String()+")")
				return
			}
		}
		if o, ok := m.b.(*WGServer); ok {
			if o.subnet().Overlaps(st.Address.Masked()) {
				httpx.WriteError(w, http.StatusConflict, "subnet overlaps "+o.st.Name+" ("+o.subnet().String()+")")
				return
			}
		}
	}
	if name := h.portUsedLocked(st.ListenPort, "udp"); name != "" {
		httpx.WriteError(w, http.StatusConflict, "UDP port "+strconv.Itoa(st.ListenPort)+" is used by "+name)
		return
	}
	if err := os.MkdirAll(wgDir(cfg, st.Type), 0o700); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s := newWGServer(cfg, h.log, st)
	s.mu.Lock()
	err = s.save(r.Context())
	s.mu.Unlock()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.mountLocked(s)
	h.log.Info("WireGuard server created", "server", st.ID, "type", st.Type, "port", st.ListenPort, "subnet", st.Address.Masked())
	httpx.WriteJSON(w, http.StatusCreated, s.Info(r.Context()))
}

func (h *Host) handleRemove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("server")
	m, ok := h.get(id)
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, "no server "+id)
		return
	}
	kind := "WireGuard"
	switch s := m.b.(type) {
	case *WGServer:
		if err := s.remove(r.Context()); err != nil {
			httpx.WriteError(w, http.StatusBadGateway, err.Error())
			return
		}
	case *Server:
		if s.extra == nil {
			httpx.WriteError(w, http.StatusConflict, "the first OpenVPN server cannot be removed")
			return
		}
		dst, err := s.remove(r.Context())
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		kind = "OpenVPN"
		h.log.Info("OpenVPN server files kept", "server", id, "dir", dst)
	}
	h.mu.Lock()
	for i, x := range h.servers {
		if x.b.ID() == id {
			h.servers = append(h.servers[:i], h.servers[i+1:]...)
			break
		}
	}
	h.mu.Unlock()
	h.log.Info(kind+" server removed", "server", id)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Host) createOpenVPN(w http.ResponseWriter, r *http.Request, req api.CreateServer) {
	h.mu.RLock()
	base := h.ovpn
	h.mu.RUnlock()
	if base == nil || base.OpenVPNServersDir == "" {
		httpx.WriteError(w, http.StatusBadRequest, "this agent cannot add OpenVPN servers (TD_OPENVPN_SERVERS_DIR)")
		return
	}
	st, err := createOpenVPN(req)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	mgmtHost, _, _ := net.SplitHostPort(base.MgmtAddr)
	if mgmtHost == "" {
		mgmtHost = "127.0.0.1"
	}
	usedMgmt := map[int]bool{}
	for _, m := range h.servers {
		if m.b.ID() == st.ID {
			httpx.WriteError(w, http.StatusConflict, "a server with id "+st.ID+" exists")
			return
		}
		if s, ok := m.b.(*Server); ok {
			usedMgmt[s.mgmtPort()] = true
		}
	}
	if name := h.portUsedLocked(st.Port, st.Proto); name != "" {
		httpx.WriteError(w, http.StatusConflict, strings.ToUpper(st.Proto)+" port "+strconv.Itoa(st.Port)+" is used by "+name)
		return
	}
	_, p, _ := net.SplitHostPort(base.MgmtAddr)
	port, _ := strconv.Atoi(p)
	if port == 0 {
		port = 2080
	}
	for usedMgmt[port] {
		port++
	}
	st.Mgmt = net.JoinHostPort(mgmtHost, strconv.Itoa(port))
	dir := filepath.Join(base.OpenVPNServersDir, st.ID)
	if _, err := os.Stat(dir); err == nil {
		httpx.WriteError(w, http.StatusConflict, dir+" exists; move it away or pick another id")
		return
	}
	data, _ := json.MarshalIndent(st, "", "  ")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := writeAtomic(filepath.Join(dir, ovpnStateFile), append(data, '\n'), 0o600); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s := newOpenVPNServer(*base, h.log, st, nil)
	h.mountLocked(s)
	h.log.Info("OpenVPN server created", "server", st.ID, "port", st.Port, "proto", st.Proto, "mgmt", st.Mgmt)
	httpx.WriteJSON(w, http.StatusCreated, s.Info(r.Context()))
}

func (h *Host) requireToken(next http.Handler) http.Handler {
	want := []byte(h.token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get(api.HeaderToken))
		if len(want) == 0 || subtle.ConstantTimeCompare(got, want) != 1 {
			httpx.WriteError(w, http.StatusUnauthorized, "invalid or missing agent token")
			return
		}
		next.ServeHTTP(w, r)
	})
}
