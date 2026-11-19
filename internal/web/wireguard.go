package web

import (
	"net/http"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/httpx"
)

func (s *Server) registerWireGuardRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/servers", s.handleCreateServer)
	mux.HandleFunc("DELETE /api/v1/servers/{server}", s.handleRemoveServer)
	mux.HandleFunc("GET /api/v1/servers/{server}/peers", s.handleListPeers)
	mux.HandleFunc("POST /api/v1/servers/{server}/peers", s.handleAddPeer)
	mux.HandleFunc("POST /api/v1/servers/{server}/peers/bulk", s.handleAddPeers)
	mux.HandleFunc("GET /api/v1/servers/{server}/settings", s.handleWGSettings)
	mux.HandleFunc("PUT /api/v1/servers/{server}/settings", s.handleSetWGSettings)
	mux.HandleFunc("GET /api/v1/servers/{server}/peers/{name}", s.handleGetPeer)
	mux.HandleFunc("PUT /api/v1/servers/{server}/peers/{name}", s.handleUpdatePeer)
	mux.HandleFunc("DELETE /api/v1/servers/{server}/peers/{name}", s.handleRemovePeer)
	mux.HandleFunc("GET /api/v1/servers/{server}/peers/{name}/conf", s.handlePeerConf)
}

func (s *Server) handleCreateServer(w http.ResponseWriter, r *http.Request) {
	var req api.CreateServer
	if !decodeJSON(w, r, &req) {
		return
	}
	info, err := s.Agent.CreateServer(r.Context(), req)
	if err != nil {
		s.agentError(w, err)
		return
	}
	s.audit(r, "server.create", info.ID)
	httpx.WriteJSON(w, http.StatusCreated, info)
}

func (s *Server) handleRemoveServer(w http.ResponseWriter, r *http.Request) {
	if err := s.agent(r).RemoveServer(r.Context()); err != nil {
		s.agentError(w, err)
		return
	}
	s.audit(r, "server.remove", r.PathValue("server"))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListPeers(w http.ResponseWriter, r *http.Request) {
	peers, err := s.agent(r).Peers(r.Context())
	if err != nil {
		s.agentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, peers)
}

func (s *Server) handleGetPeer(w http.ResponseWriter, r *http.Request) {
	name, ok := s.name(w, r)
	if !ok {
		return
	}
	p, err := s.agent(r).Peer(r.Context(), name)
	if err != nil {
		s.agentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

func (s *Server) handleAddPeer(w http.ResponseWriter, r *http.Request) {
	var req api.NewPeer
	if !decodeJSON(w, r, &req) {
		return
	}
	p, err := s.agent(r).AddPeer(r.Context(), req)
	if err != nil {
		s.agentError(w, err)
		return
	}
	s.audit(r, "peer.add", p.Name)
	httpx.WriteJSON(w, http.StatusCreated, p)
}

func (s *Server) handleUpdatePeer(w http.ResponseWriter, r *http.Request) {
	name, ok := s.name(w, r)
	if !ok {
		return
	}
	var req api.PeerUpdate
	if !decodeJSON(w, r, &req) {
		return
	}
	p, err := s.agent(r).UpdatePeer(r.Context(), name, req)
	if err != nil {
		s.agentError(w, err)
		return
	}
	s.audit(r, "peer.update", name)
	httpx.WriteJSON(w, http.StatusOK, p)
}

func (s *Server) handleRemovePeer(w http.ResponseWriter, r *http.Request) {
	name, ok := s.name(w, r)
	if !ok {
		return
	}
	if err := s.agent(r).RemovePeer(r.Context(), name); err != nil {
		s.agentError(w, err)
		return
	}
	s.audit(r, "peer.remove", name)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePeerConf(w http.ResponseWriter, r *http.Request) {
	name, ok := s.name(w, r)
	if !ok {
		return
	}
	conf, err := s.agent(r).PeerConf(r.Context(), name)
	if err != nil {
		s.agentError(w, err)
		return
	}
	s.audit(r, "peer.download", name)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.conf"`)
	_, _ = w.Write(conf)
}

func (s *Server) handleAddPeers(w http.ResponseWriter, r *http.Request) {
	var req api.NewPeers
	if !decodeJSON(w, r, &req) {
		return
	}
	peers, err := s.agent(r).AddPeers(r.Context(), req)
	if err != nil {
		s.agentError(w, err)
		return
	}
	s.audit(r, "peer.add_bulk", req.Prefix)
	httpx.WriteJSON(w, http.StatusCreated, peers)
}

func (s *Server) handleWGSettings(w http.ResponseWriter, r *http.Request) {
	set, err := s.agent(r).WGSettings(r.Context())
	if err != nil {
		s.agentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, set)
}

func (s *Server) handleSetWGSettings(w http.ResponseWriter, r *http.Request) {
	var req api.WGSettings
	if !decodeJSON(w, r, &req) {
		return
	}
	set, err := s.agent(r).SetWGSettings(r.Context(), req)
	if err != nil {
		s.agentError(w, err)
		return
	}
	s.audit(r, "server.settings", r.PathValue("server"))
	httpx.WriteJSON(w, http.StatusOK, set)
}
