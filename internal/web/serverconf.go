package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/httpx"
)

func (s *Server) registerServerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/server/config", s.handleGetServerConfig)
	mux.HandleFunc("POST /api/v1/server/config/check", s.handleCheckServerConfig)
	mux.HandleFunc("PUT /api/v1/server/config", s.handleSaveServerConfig)
	mux.HandleFunc("POST /api/v1/server/restart", s.handleRestartServer)
	mux.HandleFunc("GET /api/v1/server/backups", s.handleConfigBackups)
	mux.HandleFunc("GET /api/v1/server/backups/{name}", s.handleConfigBackup)
	mux.HandleFunc("GET /api/v1/setup", s.handleSetupStatus)
	mux.HandleFunc("POST /api/v1/setup", s.handleSetup)
	mux.HandleFunc("GET /api/v1/firewall", s.handleGetFirewall)
	mux.HandleFunc("PUT /api/v1/firewall", s.handleSetFirewall)
	mux.HandleFunc("POST /api/v1/firewall/addresses", s.handleAssignAddresses)
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

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.Agent.SetupStatus(r.Context())
	if err != nil {
		s.agentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var req api.SetupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	s.audit(r, "server.setup", req.PublicHost)
	res, err := s.Agent.Setup(r.Context(), req)
	if err != nil {
		s.agentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func (s *Server) handleGetFirewall(w http.ResponseWriter, r *http.Request) {
	st, err := s.Agent.Firewall(r.Context())
	if err != nil {
		s.agentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

func (s *Server) handleSetFirewall(w http.ResponseWriter, r *http.Request) {
	var p api.FirewallPolicy
	if !decodeJSON(w, r, &p) {
		return
	}
	st, err := s.Agent.SetFirewall(r.Context(), p)
	if err != nil {
		s.agentError(w, err)
		return
	}
	s.audit(r, "firewall.save", fmt.Sprintf("enabled=%t default=%s rules=%d", p.Enabled, p.Default, len(p.Rules)))
	httpx.WriteJSON(w, http.StatusOK, st)
}

func (s *Server) handleAssignAddresses(w http.ResponseWriter, r *http.Request) {
	var req api.AssignAddresses
	if !decodeJSON(w, r, &req) {
		return
	}
	st, err := s.Agent.AssignAddresses(r.Context(), req.Names)
	if err != nil {
		s.agentError(w, err)
		return
	}
	s.audit(r, "firewall.addresses", strings.Join(req.Names, ","))
	httpx.WriteJSON(w, http.StatusOK, st)
}
