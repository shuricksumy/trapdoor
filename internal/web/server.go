// Package web implements the unprivileged web/API process.
package web

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/shuricksumy/tunnelward/internal/api"
	"github.com/shuricksumy/tunnelward/internal/history"
	"github.com/shuricksumy/tunnelward/internal/httpx"
	"github.com/shuricksumy/tunnelward/internal/version"
)

// AgentClient is the subset of the agent API the web process uses.
// Defined here so tests can supply a fake.
type AgentClient interface {
	Info(ctx context.Context) (api.AgentInfo, error)
	Sessions(ctx context.Context) (api.Sessions, error)
}

// Deps are the web server's dependencies.
type Deps struct {
	Agent AgentClient
	Stats *history.Stats
	// APIToken protects /api/v1/*; when empty the API answers 401.
	APIToken string
	Location *time.Location
	Log      *slog.Logger
	Now      func() time.Time // nil = time.Now
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

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, apiHealth())
	})
	mux.Handle("/api/", s.requireToken(api))
	return httpx.LogRequests(s.Log, mux)
}

func apiHealth() api.Health { return api.Health{Status: "ok"} }

// requireToken checks "Authorization: Bearer <TW_WEB_API_TOKEN>".
// Temporary until admin login exists; never open without a token.
func (s *Server) requireToken(next http.Handler) http.Handler {
	want := []byte(s.APIToken)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(want) == 0 {
			httpx.WriteError(w, http.StatusUnauthorized, "API disabled: set TW_WEB_API_TOKEN")
			return
		}
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="tunnelward"`)
			httpx.WriteError(w, http.StatusUnauthorized, "invalid or missing API token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st := api.Status{WebVersion: version.Version}
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
