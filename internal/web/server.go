// Package web implements the unprivileged web/API process.
package web

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/shuricksumy/tunnelward/internal/api"
	"github.com/shuricksumy/tunnelward/internal/auth"
	"github.com/shuricksumy/tunnelward/internal/history"
	"github.com/shuricksumy/tunnelward/internal/httpx"
	"github.com/shuricksumy/tunnelward/internal/version"
	"github.com/shuricksumy/tunnelward/internal/webui"
)

// AgentClient is the subset of the agent API the web process uses.
// Defined here so tests can supply a fake.
type AgentClient interface {
	Info(ctx context.Context) (api.AgentInfo, error)
	Sessions(ctx context.Context) (api.Sessions, error)
	ListCerts(ctx context.Context) ([]api.Cert, error)
	GetCert(ctx context.Context, name string) (api.Cert, error)
	IssueCert(ctx context.Context, req api.IssueCertRequest) (api.Cert, error)
	RevokeCert(ctx context.Context, name string) (api.Cert, error)
	UnrevokeCert(ctx context.Context, name string) (api.Cert, error)
	RenewCert(ctx context.Context, name string, validityDays int) (api.Cert, error)
	RemoveCert(ctx context.Context, name string) error
	Profile(ctx context.Context, name string) ([]byte, error)
	KillSession(ctx context.Context, clientID int64) error
	KillName(ctx context.Context, name string) (int, error)
	ClientAuth(ctx context.Context, name string) (api.ClientAuth, error)
	UpdateClientAuth(ctx context.Context, name string, req api.ClientAuthUpdate) (api.ClientAuth, error)
	ServerNetwork(ctx context.Context) (api.ServerNetwork, error)
	ClientNetwork(ctx context.Context, name string) (api.ClientNetwork, error)
	SetClientNetwork(ctx context.Context, name string, n api.ClientNetwork) (api.ClientNetwork, error)
}

// Deps are the web server's dependencies.
type Deps struct {
	Agent   AgentClient
	Stats   *history.Stats
	Auth    *auth.Service
	Limiter *auth.Limiter
	// APIToken optionally allows scripts with "Authorization: Bearer"; empty = off.
	APIToken string
	// CookieSecure marks the session cookie Secure (HTTPS only).
	CookieSecure bool
	// TrustedProxies may set X-Forwarded-For (for login throttling and logs).
	TrustedProxies []netip.Prefix
	// ExpiryWarnDays flags certificates expiring within this many days.
	ExpiryWarnDays int
	Location       *time.Location
	Log            *slog.Logger
	Now            func() time.Time // nil = time.Now
}

// Server is the web HTTP server.
type Server struct {
	Deps
}

// New returns a web server.
func New(d Deps) *Server {
	if d.Location == nil {
		d.Location = time.Local
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Limiter == nil {
		d.Limiter = &auth.Limiter{}
	}
	if d.ExpiryWarnDays == 0 {
		d.ExpiryWarnDays = 30
	}
	return &Server{Deps: d}
}

// Handler returns the web process HTTP handler.
func (s *Server) Handler() http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/status", s.handleStatus)
	api.HandleFunc("GET /api/v1/sessions", s.handleLiveSessions)
	api.HandleFunc("GET /api/v1/stats", s.handleStats)
	api.HandleFunc("GET /api/v1/stats.csv", s.handleStatsCSV)
	api.HandleFunc("GET /api/v1/history/sessions", s.handleHistorySessions)
	s.registerCertRoutes(api)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, apiHealth())
	})
	s.registerAuthRoutes(mux, api)
	mux.Handle("/api/", s.requireAuth(api))
	ui := webui.Handler()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		ui.ServeHTTP(w, r)
	})
	return httpx.LogRequests(s.Log, securityHeaders(mux))
}

func apiHealth() api.Health { return api.Health{Status: "ok"} }

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st := api.Status{WebVersion: version.Version, ExpiryWarnDays: s.ExpiryWarnDays}
	info, err := s.Agent.Info(r.Context())
	if err != nil {
		s.Log.Warn("agent unreachable", "err", err)
		st.AgentError = err.Error()
	} else {
		st.AgentOK = true
		st.Agent = &info
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

func (s *Server) handleLiveSessions(w http.ResponseWriter, r *http.Request) {
	live, err := s.Agent.Sessions(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, live)
}
