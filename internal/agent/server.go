// Package agent implements the privileged tunnelward agent and its HTTP API.
package agent

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"os"

	"github.com/shuricksumy/tunnelward/internal/api"
	"github.com/shuricksumy/tunnelward/internal/config"
	"github.com/shuricksumy/tunnelward/internal/httpx"
	"github.com/shuricksumy/tunnelward/internal/version"
)

// Server is the agent HTTP API.
type Server struct {
	cfg config.Agent
	log *slog.Logger
}

// New returns an agent server for cfg.
func New(cfg config.Agent, log *slog.Logger) *Server {
	return &Server{cfg: cfg, log: log}
}

// Handler returns the agent's HTTP handler.
//
// /healthz is unauthenticated (for container health checks);
// everything under /v1/ requires the shared token.
func (s *Server) Handler() http.Handler {
	v1 := http.NewServeMux()
	v1.HandleFunc("GET /v1/info", s.handleInfo)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, api.Health{Status: "ok"})
	})
	mux.Handle("/v1/", s.requireToken(v1))

	return httpx.LogRequests(s.log, mux)
}

func (s *Server) handleInfo(w http.ResponseWriter, _ *http.Request) {
	host, _ := os.Hostname()
	httpx.WriteJSON(w, http.StatusOK, api.AgentInfo{
		Version:    version.Version,
		Hostname:   host,
		OpenVPNDir: s.cfg.OpenVPNDir,
		PKIDir:     s.cfg.PKIDir,
	})
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
