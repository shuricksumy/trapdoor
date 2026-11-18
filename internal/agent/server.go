// Package agent implements the privileged tunnelward agent and its HTTP API.
package agent

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/shuricksumy/tunnelward/internal/api"
	"github.com/shuricksumy/tunnelward/internal/ccd"
	"github.com/shuricksumy/tunnelward/internal/clientauth"
	"github.com/shuricksumy/tunnelward/internal/config"
	"github.com/shuricksumy/tunnelward/internal/httpx"
	"github.com/shuricksumy/tunnelward/internal/mgmt"
	"github.com/shuricksumy/tunnelward/internal/ovpn"
	"github.com/shuricksumy/tunnelward/internal/pki"
	"github.com/shuricksumy/tunnelward/internal/serverconf"
	"github.com/shuricksumy/tunnelward/internal/version"
)

// Server is the agent HTTP API.
type Server struct {
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

	fwMu      sync.Mutex
	fwApplied string // hash of the rules applied in direct mode
	fwError   string

	restartMu   sync.Mutex
	restartWait time.Duration // tests shorten it
}

// Profiles builds client .ovpn files; *ovpn.Generator implements it.
type Profiles interface {
	Build(ctx context.Context, name string, b pki.Bundle) ([]byte, error)
	RemoveClient(name string) error
}

// New returns an agent server for cfg. p may be nil when no PKI exists yet;
// certificate endpoints then answer 503.
func New(cfg config.Agent, log *slog.Logger, p pki.PKI) *Server {
	files := &ccd.Store{Dir: cfg.CCDDir}
	return &Server{cfg: cfg, log: log, pkiv: p,
		gen:  &ovpn.Generator{Dir: cfg.OpenVPNDir, OpenVPNBin: cfg.OpenVPNBin},
		mgmt: &mgmt.Client{Addr: cfg.MgmtAddr, Password: cfg.MgmtPassword},
		ccd:  files,
		auth: &clientauth.Store{Files: files},
		conf: &serverconf.File{Path: filepath.Join(cfg.OpenVPNDir, "server.conf")}}
}

// Handler returns the agent's HTTP handler.
//
// /healthz is unauthenticated (for container health checks);
// everything under /v1/ requires the shared token.
func (s *Server) Handler() http.Handler {
	v1 := http.NewServeMux()
	v1.HandleFunc("GET /v1/info", s.handleInfo)
	s.registerCertRoutes(v1)
	s.registerSessionRoutes(v1)
	s.registerAuthRoutes(v1)
	s.registerNetworkRoutes(v1)
	s.registerServerConfigRoutes(v1)
	s.registerSetupRoutes(v1)
	s.registerFirewallRoutes(v1)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, api.Health{Status: "ok"})
	})
	mux.Handle("/v1/", s.requireToken(v1))

	return httpx.LogRequests(s.log, mux)
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	host, _ := os.Hostname()
	info := api.AgentInfo{
		Version:    version.Version,
		Hostname:   host,
		OpenVPNDir: s.cfg.OpenVPNDir,
		PKIDir:     s.cfg.PKIDir,
		PKIReady:   s.PKI() != nil,
	}
	if s.PKI() != nil {
		if ca, err := s.PKI().CAInfo(r.Context()); err == nil {
			info.CAName, info.CANotAfter = ca.Name, ca.NotAfter
		}
	}
	if v, err := s.mgmt.Version(r.Context()); err != nil {
		info.MgmtError = err.Error()
	} else {
		info.OpenVPNVersion = v
	}
	httpx.WriteJSON(w, http.StatusOK, info)
}

func (s *Server) requireToken(next http.Handler) http.Handler {
	want := []byte(s.cfg.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get(api.HeaderToken))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			httpx.WriteError(w, http.StatusUnauthorized, "invalid or missing agent token")
			return
		}
		next.ServeHTTP(w, r)
	})
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
