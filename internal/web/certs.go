package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/shuricksumy/trapdoor/internal/agent"
	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/httpx"
	"github.com/shuricksumy/trapdoor/internal/pki"
)

func (s *Server) registerCertRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/servers/{server}/certs", s.handleListCerts)
	mux.HandleFunc("POST /api/v1/servers/{server}/certs", s.handleIssueCert)
	mux.HandleFunc("POST /api/v1/servers/{server}/certs/renew", s.handleRenewMany)
	mux.HandleFunc("GET /api/v1/servers/{server}/certs/{name}", s.handleGetCert)
	mux.HandleFunc("DELETE /api/v1/servers/{server}/certs/{name}", s.handleRemoveCert)
	mux.HandleFunc("POST /api/v1/servers/{server}/certs/{name}/revoke", s.handleRevokeCert)
	mux.HandleFunc("POST /api/v1/servers/{server}/certs/{name}/unrevoke", s.handleUnrevokeCert)
	mux.HandleFunc("POST /api/v1/servers/{server}/certs/{name}/renew", s.handleRenewCert)
	mux.HandleFunc("GET /api/v1/servers/{server}/certs/{name}/ovpn", s.handleProfile)
	mux.HandleFunc("DELETE /api/v1/servers/{server}/certs/{name}/sessions", s.handleKillName)
	mux.HandleFunc("DELETE /api/v1/servers/{server}/sessions/{id}", s.handleKillSession)
	mux.HandleFunc("GET /api/v1/servers/{server}/certs/{name}/auth", s.handleGetClientAuth)
	mux.HandleFunc("PUT /api/v1/servers/{server}/certs/{name}/auth", s.handleUpdateClientAuth)
	mux.HandleFunc("GET /api/v1/servers/{server}/network", s.handleServerNetwork)
	mux.HandleFunc("GET /api/v1/servers/{server}/certs/{name}/network", s.handleGetClientNetwork)
	mux.HandleFunc("PUT /api/v1/servers/{server}/certs/{name}/network", s.handleSetClientNetwork)
	mux.HandleFunc("GET /api/v1/servers/{server}/named-networks", s.handleNamedNetworks)
	mux.HandleFunc("GET /api/v1/servers/{server}/network/clients", s.handleClientsNetwork)
	mux.HandleFunc("PUT /api/v1/servers/{server}/named-networks/{network}", s.handleSetNamedNetwork)
	mux.HandleFunc("DELETE /api/v1/servers/{server}/named-networks/{network}", s.handleRemoveNamedNetwork)
}

func (s *Server) handleServerNetwork(w http.ResponseWriter, r *http.Request) {
	n, err := s.agent(r).ServerNetwork(r.Context())
	if err != nil {
		s.agentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, n)
}

func (s *Server) handleGetClientNetwork(w http.ResponseWriter, r *http.Request) {
	name, ok := s.name(w, r)
	if !ok {
		return
	}
	n, err := s.agent(r).ClientNetwork(r.Context(), name)
	if err != nil {
		s.agentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, n)
}

func (s *Server) handleSetClientNetwork(w http.ResponseWriter, r *http.Request) {
	name, ok := s.name(w, r)
	if !ok {
		return
	}
	var req api.ClientNetwork
	if !decodeJSON(w, r, &req) {
		return
	}
	n, err := s.agent(r).SetClientNetwork(r.Context(), name, req)
	if err != nil {
		s.agentError(w, err)
		return
	}
	s.audit(r, "client.network", name)
	httpx.WriteJSON(w, http.StatusOK, n)
}

func (s *Server) handleClientsNetwork(w http.ResponseWriter, r *http.Request) {
	n, err := s.agent(r).ClientsNetwork(r.Context())
	if err != nil {
		s.agentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, n)
}

func (s *Server) handleNamedNetworks(w http.ResponseWriter, r *http.Request) {
	n, err := s.agent(r).NamedNetworks(r.Context())
	if err != nil {
		s.agentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, n)
}

func (s *Server) handleSetNamedNetwork(w http.ResponseWriter, r *http.Request) {
	var req api.NamedNetwork
	if !decodeJSON(w, r, &req) {
		return
	}
	n, err := s.agent(r).SetNamedNetwork(r.Context(), r.PathValue("network"), req)
	if err != nil {
		s.agentError(w, err)
		return
	}
	s.audit(r, "network.save", r.PathValue("network"))
	httpx.WriteJSON(w, http.StatusOK, n)
}

func (s *Server) handleRemoveNamedNetwork(w http.ResponseWriter, r *http.Request) {
	n, err := s.agent(r).RemoveNamedNetwork(r.Context(), r.PathValue("network"))
	if err != nil {
		s.agentError(w, err)
		return
	}
	s.audit(r, "network.remove", r.PathValue("network"))
	httpx.WriteJSON(w, http.StatusOK, n)
}

func (s *Server) handleGetClientAuth(w http.ResponseWriter, r *http.Request) {
	name, ok := s.name(w, r)
	if !ok {
		return
	}
	st, err := s.agent(r).ClientAuth(r.Context(), name)
	if err != nil {
		s.agentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

func (s *Server) handleUpdateClientAuth(w http.ResponseWriter, r *http.Request) {
	name, ok := s.name(w, r)
	if !ok {
		return
	}
	var req api.ClientAuthUpdate
	if !decodeJSON(w, r, &req) {
		return
	}
	st, err := s.agent(r).UpdateClientAuth(r.Context(), name, req)
	if err != nil {
		s.agentError(w, err)
		return
	}
	action := "client2fa.update"
	if req.TOTP != "" {
		action = "client2fa.totp-" + req.TOTP
	}
	s.audit(r, action, name)
	httpx.WriteJSON(w, http.StatusOK, st)
}

// name returns the {name} path value, or writes 400 if it is not a valid
// certificate name (checked here too, not only by the agent).
func (s *Server) name(w http.ResponseWriter, r *http.Request) (string, bool) {
	n := r.PathValue("name")
	if !pki.ValidName(n) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid certificate name")
		return "", false
	}
	return n, true
}

// audit logs a change with the user who made it.
func (s *Server) audit(r *http.Request, action, target string) {
	p := principal(r)
	s.Log.Info("audit", "server", r.PathValue("server"), "action", action, "target", target, "by", p.Username, "via", p.Method, "ip", s.clientIP(r))
}

// agentError forwards the agent's 4xx answers; anything else is a 502.
func (s *Server) agentError(w http.ResponseWriter, err error) {
	var ae *agent.APIError
	if errors.As(err, &ae) && ae.Status >= 400 && ae.Status < 500 && ae.Status != http.StatusUnauthorized {
		httpx.WriteError(w, ae.Status, ae.Message)
		return
	}
	if errors.As(err, &ae) && ae.Status == http.StatusServiceUnavailable {
		httpx.WriteError(w, http.StatusServiceUnavailable, ae.Message)
		return
	}
	s.Log.Warn("agent call failed", "err", err)
	httpx.WriteError(w, http.StatusBadGateway, "agent: "+err.Error())
}

func (s *Server) handleListCerts(w http.ResponseWriter, r *http.Request) {
	certs, err := s.agent(r).ListCerts(r.Context())
	if err != nil {
		s.agentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, certs)
}

func (s *Server) handleGetCert(w http.ResponseWriter, r *http.Request) {
	name, ok := s.name(w, r)
	if !ok {
		return
	}
	c, err := s.agent(r).GetCert(r.Context(), name)
	if err != nil {
		s.agentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, c)
}

func (s *Server) handleIssueCert(w http.ResponseWriter, r *http.Request) {
	var req api.IssueCertRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !pki.ValidName(req.Name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid certificate name")
		return
	}
	c, err := s.agent(r).IssueCert(r.Context(), req)
	if err != nil {
		s.agentError(w, err)
		return
	}
	s.audit(r, "cert.issue", c.Name)
	httpx.WriteJSON(w, http.StatusCreated, c)
}

func (s *Server) handleRevokeCert(w http.ResponseWriter, r *http.Request) {
	name, ok := s.name(w, r)
	if !ok {
		return
	}
	c, err := s.agent(r).RevokeCert(r.Context(), name)
	if err != nil {
		s.agentError(w, err)
		return
	}
	s.audit(r, "cert.revoke", name)
	httpx.WriteJSON(w, http.StatusOK, c)
}

func (s *Server) handleUnrevokeCert(w http.ResponseWriter, r *http.Request) {
	name, ok := s.name(w, r)
	if !ok {
		return
	}
	c, err := s.agent(r).UnrevokeCert(r.Context(), name)
	if err != nil {
		s.agentError(w, err)
		return
	}
	s.audit(r, "cert.unrevoke", name)
	httpx.WriteJSON(w, http.StatusOK, c)
}

func (s *Server) handleRenewCert(w http.ResponseWriter, r *http.Request) {
	name, ok := s.name(w, r)
	if !ok {
		return
	}
	var req api.RenewCertRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	c, err := s.agent(r).RenewCert(r.Context(), name, req.ValidityDays)
	if err != nil {
		s.agentError(w, err)
		return
	}
	s.audit(r, "cert.renew", name)
	httpx.WriteJSON(w, http.StatusOK, c)
}

// handleRenewMany renews several certificates; each result is reported
// separately, so one failure does not stop the others.
func (s *Server) handleRenewMany(w http.ResponseWriter, r *http.Request) {
	var req api.RenewManyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Names) == 0 || len(req.Names) > 500 {
		httpx.WriteError(w, http.StatusBadRequest, "names must list 1-500 certificates")
		return
	}
	seen := map[string]bool{}
	results := make([]api.RenewResult, 0, len(req.Names))
	for _, name := range req.Names {
		if seen[name] {
			continue
		}
		seen[name] = true
		res := api.RenewResult{Name: name}
		if !pki.ValidName(name) {
			res.Error = "invalid certificate name"
			results = append(results, res)
			continue
		}
		c, err := s.agent(r).RenewCert(r.Context(), name, req.ValidityDays)
		if err != nil {
			var ae *agent.APIError
			if errors.As(err, &ae) {
				res.Error = ae.Message
			} else {
				res.Error = err.Error()
			}
		} else {
			res.OK, res.Cert = true, &c
			s.audit(r, "cert.renew", name)
		}
		results = append(results, res)
	}
	httpx.WriteJSON(w, http.StatusOK, results)
}

func (s *Server) handleRemoveCert(w http.ResponseWriter, r *http.Request) {
	name, ok := s.name(w, r)
	if !ok {
		return
	}
	if err := s.agent(r).RemoveCert(r.Context(), name); err != nil {
		s.agentError(w, err)
		return
	}
	_ = s.Stats.DB.SetClientEmail(r.Context(), r.PathValue("server"), name, "")
	s.audit(r, "cert.remove", name)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	name, ok := s.name(w, r)
	if !ok {
		return
	}
	profile, err := s.agent(r).Profile(r.Context(), name)
	if err != nil {
		s.agentError(w, err)
		return
	}
	s.audit(r, "cert.download", name)
	w.Header().Set("Content-Type", "application/x-openvpn-profile")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.ovpn"`)
	_, _ = w.Write(profile)
}

func (s *Server) handleKillName(w http.ResponseWriter, r *http.Request) {
	name, ok := s.name(w, r)
	if !ok {
		return
	}
	n, err := s.agent(r).KillName(r.Context(), name)
	if err != nil {
		s.agentError(w, err)
		return
	}
	s.audit(r, "session.kill", name)
	httpx.WriteJSON(w, http.StatusOK, api.Killed{Killed: n})
}

func (s *Server) handleKillSession(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 0 {
		httpx.WriteError(w, http.StatusBadRequest, "session id must be a client ID number")
		return
	}
	if err := s.agent(r).KillSession(r.Context(), id); err != nil {
		s.agentError(w, err)
		return
	}
	s.audit(r, "session.kill", "client_id="+strconv.FormatInt(id, 10))
	w.WriteHeader(http.StatusNoContent)
}
