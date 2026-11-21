// Package agent implements the privileged trapdoor agent and its HTTP API.
package agent

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"path/filepath"
	"sync"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/ccd"
	"github.com/shuricksumy/trapdoor/internal/ccdnet"
	"github.com/shuricksumy/trapdoor/internal/clientauth"
	"github.com/shuricksumy/trapdoor/internal/config"
	"github.com/shuricksumy/trapdoor/internal/firewall"
	"github.com/shuricksumy/trapdoor/internal/httpx"
	"github.com/shuricksumy/trapdoor/internal/mgmt"
	"github.com/shuricksumy/trapdoor/internal/ovpn"
	"github.com/shuricksumy/trapdoor/internal/pki"
	"github.com/shuricksumy/trapdoor/internal/serverconf"
	"github.com/shuricksumy/trapdoor/internal/shaping"
)

// Server is one OpenVPN server managed by the agent: its PKI, server.conf,
// ccd files, management interface and access rules. A Host serves its API
// under /v1/servers/{id}/.
type Server struct {
	id    string
	name  string
	cfg   config.Agent
	log   *slog.Logger
	pkiMu sync.RWMutex
	pkiv  pki.PKI // nil until the PKI exists; use PKI()
	gen   Profiles
	mgmt  Management
	ccd   *ccd.Store
	auth  *clientauth.Store
	conf  *serverconf.File

	netMu sync.Mutex // static address changes

	topoOnce sync.Once
	topo     ccdnet.Topology // see defaultTopology

	// dev is the tun device: "tun" (OpenVPN picks tun0, tun1, …) for the
	// first server, the server id for the ones added in trapdoor, so each
	// has its own access rules chain.
	dev     string
	fwChain string // TD-FWD, or TD-<id>
	fwIface string // tun+, or the id
	// extra is the state of a server added in trapdoor (nil for the first).
	extra *ovpnState
	// others returns the subnets of the other servers (set by the Host).
	others func(self string) []netip.Prefix

	fwMu sync.Mutex
	fw   *fwTarget // use firewallTarget()

	shapeOnce sync.Once
	shape     *shaper // use shaper()

	restartMu   sync.Mutex
	restartWait time.Duration // tests shorten it
}

// Profiles builds client .ovpn files; *ovpn.Generator implements it.
type Profiles interface {
	Build(ctx context.Context, name string, b pki.Bundle) ([]byte, error)
	RemoveClient(name string) error
}

// DefaultServerID is the id of the OpenVPN server configured by the TD_*
// settings (the first server; setups from before servers existed).
const DefaultServerID = "openvpn"

// New returns the OpenVPN server described by cfg, with id DefaultServerID.
// p may be nil when no PKI exists yet; certificate endpoints then answer 503.
func New(cfg config.Agent, log *slog.Logger, p pki.PKI) *Server {
	return newServer(DefaultServerID, "OpenVPN", cfg, log, p)
}

func newServer(id, name string, cfg config.Agent, log *slog.Logger, p pki.PKI) *Server {
	files := &ccd.Store{Dir: cfg.CCDDir}
	return &Server{id: id, name: name, cfg: cfg, log: log.With("server", id), pkiv: p,
		dev: "tun", fwChain: firewall.Chain, fwIface: "tun+",
		gen:  &ovpn.Generator{Dir: cfg.OpenVPNDir, OpenVPNBin: cfg.OpenVPNBin},
		mgmt: &mgmt.Client{Addr: cfg.MgmtAddr, Password: cfg.MgmtPassword},
		ccd:  files,
		auth: &clientauth.Store{Files: files},
		conf: &serverconf.File{Path: filepath.Join(cfg.OpenVPNDir, "server.conf")}}
}

// ID returns the server's id.
func (s *Server) ID() string { return s.id }

// routes returns the server's API, relative to /v1/servers/{id}.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /info", s.handleInfo)
	mux.HandleFunc("GET /backup", s.handleBackup)
	s.registerCertRoutes(mux)
	s.registerSessionRoutes(mux)
	s.registerAuthRoutes(mux)
	s.registerNetworkRoutes(mux)
	s.registerNamedRoutes(mux)
	s.registerServerConfigRoutes(mux)
	s.registerSetupRoutes(mux)
	s.registerFirewallRoutes(mux)
	s.registerControlRoutes(mux)
	registerScripts(mux, s.scripts)
	registerTools(mux)
	registerShaping(mux, s.shaper())
	return mux
}

// shaper keeps the server's speed limits (trapdoor-shaping.*), matched by
// the clients' fixed addresses and, for the others, the addresses they
// have while connected.
func (s *Server) shaper() *shaper {
	s.shapeOnce.Do(func() {
		s.shape = &shaper{mode: s.firewallMode(), iface: s.fwIface, ifb: shaping.IFB(s.id),
			base: filepath.Join(s.cfg.OpenVPNDir, "trapdoor-"), waitFn: s.firewallWait, addrs: s.clientAddrs, log: s.log}
	})
	return s.shape
}

func (s *Server) clientAddrs(ctx context.Context) (map[string]netip.Addr, error) {
	ips, err := s.staticIPs()
	if err != nil {
		return nil, err
	}
	if s.stopped() {
		return ips, nil
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if st, err := s.mgmt.Status(cctx); err == nil {
		for _, se := range st.Sessions {
			if _, fixed := ips[se.Name]; fixed {
				continue
			}
			if a, err := netip.ParseAddr(se.VirtualAddress); err == nil {
				ips[se.Name] = a
			}
		}
	}
	return ips, nil
}

// Info describes the server; it never fails, problems are in the fields.
func (s *Server) Info(ctx context.Context) api.ServerInfo {
	info := api.ServerInfo{
		ID:         s.id,
		Type:       api.TypeOpenVPN,
		Name:       s.name,
		OpenVPNDir: s.cfg.OpenVPNDir,
		PKIDir:     s.cfg.PKIDir,
		PKIReady:   s.PKI() != nil,
		Stopped:    s.stopped(),
		Container:  s.cfg.ContainerOpenVPN,
		Autostart:  s.autostart(),
	}
	if s.extra != nil {
		info.ListenPort, info.Proto = s.extra.Port, s.extra.Proto
	} else if sc, err := s.settings(); err == nil {
		info.ListenPort, info.Proto = sc.Port, sc.Proto
	}
	if p := s.PKI(); p != nil {
		if ca, err := p.CAInfo(ctx); err == nil {
			info.CAName, info.CANotAfter = ca.Name, ca.NotAfter
		}
	}
	if info.Stopped {
		// Not asked: not running is expected.
	} else if v, err := s.mgmt.Version(ctx); err != nil {
		info.MgmtError = err.Error()
	} else {
		info.OpenVPNVersion = v
	}
	return info
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, s.Info(r.Context()))
}

// PKI returns the certificate authority, or nil before setup.
func (s *Server) PKI() pki.PKI {
	s.pkiMu.RLock()
	defer s.pkiMu.RUnlock()
	return s.pkiv
}

func (s *Server) setPKI(p pki.PKI) {
	s.pkiMu.Lock()
	s.pkiv = p
	s.pkiMu.Unlock()
}

// scripts are the server's presets and own start / stop scripts, run by the
// vpn container (TD_FIREWALL=file) while its tun device is up.
func (s *Server) scripts() scripts {
	iface := s.fwIface
	if iface == "" {
		iface = "tun+"
	}
	return scripts{dir: s.cfg.OpenVPNDir, prefix: "trapdoor-", id: s.id, typ: api.TypeOpenVPN, iface: iface,
		editable: s.cfg.Scripts, supported: s.cfg.Firewall == FirewallFile}
}
