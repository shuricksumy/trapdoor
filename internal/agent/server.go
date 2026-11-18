// Package agent implements the privileged trapdoor agent and its HTTP API.
package agent

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/ccd"
	"github.com/shuricksumy/trapdoor/internal/ccdnet"
	"github.com/shuricksumy/trapdoor/internal/clientauth"
	"github.com/shuricksumy/trapdoor/internal/config"
	"github.com/shuricksumy/trapdoor/internal/httpx"
	"github.com/shuricksumy/trapdoor/internal/mgmt"
	"github.com/shuricksumy/trapdoor/internal/ovpn"
	"github.com/shuricksumy/trapdoor/internal/pki"
	"github.com/shuricksumy/trapdoor/internal/serverconf"
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

	fwMu sync.Mutex
	fw   *fwTarget // use firewallTarget()

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
	files := &ccd.Store{Dir: cfg.CCDDir}
	return &Server{id: DefaultServerID, name: "OpenVPN", cfg: cfg, log: log.With("server", DefaultServerID), pkiv: p,
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
	s.registerCertRoutes(mux)
	s.registerSessionRoutes(mux)
	s.registerAuthRoutes(mux)
	s.registerNetworkRoutes(mux)
	s.registerServerConfigRoutes(mux)
	s.registerSetupRoutes(mux)
	s.registerFirewallRoutes(mux)
	return mux
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
	}
	if p := s.PKI(); p != nil {
		if ca, err := p.CAInfo(ctx); err == nil {
			info.CAName, info.CANotAfter = ca.Name, ca.NotAfter
		}
	}
	if v, err := s.mgmt.Version(ctx); err != nil {
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
