package web

import (
	"net/http"

	"github.com/shuricksumy/tunnelward/internal/api"
	"github.com/shuricksumy/tunnelward/internal/httpx"
)

func (s *Server) registerServerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/server/config", s.handleGetServerConfig)
	mux.HandleFunc("POST /api/v1/server/config/check", s.handleCheckServerConfig)
	mux.HandleFunc("PUT /api/v1/server/config", s.handleSaveServerConfig)
	mux.HandleFunc("POST /api/v1/server/restart", s.handleRestartServer)
	mux.HandleFunc("GET /api/v1/server/backups", s.handleConfigBackups)
	mux.HandleFunc("GET /api/v1/server/backups/{name}", s.handleConfigBackup)
}

func (s *Server) handleGetServerConfig(w http.ResponseWriter, r *http.Request) {
	c, err := s.Agent.ServerConfig(r.Context())
	if err != nil {
		s.agentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, c)
}

func (s *Server) handleCheckServerConfig(w http.ResponseWriter, r *http.Request) {
	var u api.ServerConfigUpdate
	if !decodeJSON(w, r, &u) {
		return
	}
	c, err := s.Agent.CheckServerConfig(r.Context(), u)
	if err != nil {
		s.agentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, c)
}

// handleSaveServerConfig checks first so the answer can list the problems.
func (s *Server) handleSaveServerConfig(w http.ResponseWriter, r *http.Request) {
	var u api.ServerConfigUpdate
	if !decodeJSON(w, r, &u) {
		return
	}
	chk, err := s.Agent.CheckServerConfig(r.Context(), u)
	if err != nil {
		s.agentError(w, err)
		return
	}
	for _, p := range chk.Problems {
		if p.Error {
			httpx.WriteJSON(w, http.StatusUnprocessableEntity, struct {
				Error    string              `json:"error"`
				Problems []api.ConfigProblem `json:"problems"`
			}{"the config has errors and was not saved", chk.Problems})
			return
		}
	}
	c, err := s.Agent.SaveServerConfig(r.Context(), u)
	if err != nil {
		s.agentError(w, err)
		return
	}
	s.audit(r, "server.config.save", "server.conf")
	httpx.WriteJSON(w, http.StatusOK, c)
}

func (s *Server) handleRestartServer(w http.ResponseWriter, r *http.Request) {
	var req api.RestartRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	s.audit(r, "server.restart", "openvpn")
	res, err := s.Agent.RestartServer(r.Context(), req)
	if err != nil {
		s.agentError(w, err)
		return
	}
	if !res.OK {
		s.Log.Warn("OpenVPN restart failed", "error", res.Error, "rolled_back", res.RolledBack)
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func (s *Server) handleConfigBackups(w http.ResponseWriter, r *http.Request) {
	list, err := s.Agent.ConfigBackups(r.Context())
	if err != nil {
		s.agentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, list)
}

func (s *Server) handleConfigBackup(w http.ResponseWriter, r *http.Request) {
	raw, err := s.Agent.ConfigBackup(r.Context(), r.PathValue("name"))
	if err != nil {
		s.agentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, struct {
		Raw string `json:"raw"`
	}{raw})
}
