// Package web implements the unprivileged web/API process.
package web

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/shuricksumy/tunnelward/internal/api"
	"github.com/shuricksumy/tunnelward/internal/httpx"
	"github.com/shuricksumy/tunnelward/internal/version"
)

// AgentClient is the subset of the agent API the web process uses.
// Defined here so tests can supply a fake.
type AgentClient interface {
	Info(ctx context.Context) (api.AgentInfo, error)
}

// Server is the web HTTP server.
type Server struct {
	agent AgentClient
	log   *slog.Logger
}

// New returns a web server that talks to agent.
func New(agent AgentClient, log *slog.Logger) *Server {
	return &Server{agent: agent, log: log}
}

// Handler returns the web process HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, api.Health{Status: "ok"})
	})
	mux.HandleFunc("GET /api/v1/status", s.handleStatus)
	return httpx.LogRequests(s.log, mux)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st := api.Status{WebVersion: version.Version}
	info, err := s.agent.Info(r.Context())
	if err != nil {
		s.log.Warn("agent unreachable", "err", err)
		st.AgentError = err.Error()
	} else {
		st.AgentOK = true
		st.Agent = &info
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}
