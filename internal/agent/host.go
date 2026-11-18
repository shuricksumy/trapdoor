package agent

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"os"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/httpx"
	"github.com/shuricksumy/trapdoor/internal/version"
)

// Host is the agent HTTP API: the VPN servers on this machine, each under
// /v1/servers/{id}/.
type Host struct {
	token   string
	log     *slog.Logger
	servers []*Server
}

// NewHost returns the API for servers; token is the shared secret the web
// process sends.
func NewHost(token string, log *slog.Logger, servers ...*Server) *Host {
	return &Host{token: token, log: log, servers: servers}
}

// Server returns the server with the given id, or nil.
func (h *Host) Server(id string) *Server {
	for _, s := range h.servers {
		if s.id == id {
			return s
		}
	}
	return nil
}

// Handler returns the agent's HTTP handler.
//
// /healthz is unauthenticated (for container health checks);
// everything under /v1/ requires the shared token.
func (h *Host) Handler() http.Handler {
	routes := make(map[string]http.Handler, len(h.servers))
	for _, s := range h.servers {
		routes[s.id] = s.routes()
	}

	v1 := http.NewServeMux()
	v1.HandleFunc("GET /v1/info", h.handleInfo)
	v1.HandleFunc("/v1/servers/{server}/", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("server")
		next, ok := routes[id]
		if !ok {
			httpx.WriteError(w, http.StatusNotFound, "no server "+id)
			return
		}
		http.StripPrefix("/v1/servers/"+id, next).ServeHTTP(w, r)
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
	for _, s := range h.servers {
		info.Servers = append(info.Servers, s.Info(r.Context()))
	}
	httpx.WriteJSON(w, http.StatusOK, info)
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
