package agent

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/httpx"
	"github.com/shuricksumy/trapdoor/internal/mgmt"
	"github.com/shuricksumy/trapdoor/internal/pki"
)

// Management is the OpenVPN management interface; *mgmt.Client implements it.
type Management interface {
	Status(ctx context.Context) (mgmt.Status, error)
	LoadStats(ctx context.Context) (mgmt.LoadStats, error)
	Version(ctx context.Context) (string, error)
	KillClient(ctx context.Context, clientID int64) error
	KillName(ctx context.Context, name string) (int, error)
	Signal(ctx context.Context, sig string) error
}

func (s *Server) registerSessionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/sessions", s.handleSessions)
	mux.HandleFunc("DELETE /v1/sessions/{id}", s.handleKillSession)
	mux.HandleFunc("DELETE /v1/certs/{name}/sessions", s.handleKillName)
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	st, err := s.mgmt.Status(r.Context())
	if err != nil {
		s.mgmtError(w, err)
		return
	}
	ls, err := s.mgmt.LoadStats(r.Context())
	if err != nil {
		s.mgmtError(w, err)
		return
	}
	out := api.Sessions{
		Time: st.Time, Clients: ls.Clients, BytesIn: ls.BytesIn, BytesOut: ls.BytesOut,
		Sessions: make([]api.Session, 0, len(st.Sessions)),
	}
	for _, x := range st.Sessions {
		out.Sessions = append(out.Sessions, api.Session{
			ClientID: x.ClientID, Name: x.Name, Username: x.Username,
			RealAddress: x.RealAddress, VirtualAddress: x.VirtualAddress, VirtualIPv6: x.VirtualIPv6,
			BytesReceived: x.BytesReceived, BytesSent: x.BytesSent,
			ConnectedSince: x.ConnectedSince, Cipher: x.Cipher,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleKillSession(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 0 {
		httpx.WriteError(w, http.StatusBadRequest, "session id must be a client ID number")
		return
	}
	if err := s.mgmt.KillClient(r.Context(), id); err != nil {
		if errors.Is(err, mgmt.ErrCommand) {
			httpx.WriteError(w, http.StatusNotFound, err.Error())
			return
		}
		s.mgmtError(w, err)
		return
	}
	s.log.Info("session killed", "client_id", id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleKillName(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !pki.ValidName(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}
	n, err := s.mgmt.KillName(r.Context(), name)
	if err != nil {
		s.mgmtError(w, err)
		return
	}
	s.log.Info("sessions killed", "name", name, "count", n)
	httpx.WriteJSON(w, http.StatusOK, api.Killed{Killed: n})
}

// disconnect kicks all sessions of name after a revoke/remove. Best effort:
// OpenVPN would reject the client at its next renegotiation anyway.
func (s *Server) disconnect(ctx context.Context, name string) {
	n, err := s.mgmt.KillName(ctx, name)
	switch {
	case err != nil:
		s.log.Warn("could not disconnect sessions", "name", name, "err", err)
	case n > 0:
		s.log.Info("disconnected sessions", "name", name, "count", n)
	}
}

func (s *Server) mgmtError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, mgmt.ErrUnavailable) {
		status = http.StatusBadGateway
	}
	s.log.Warn("management", "err", err)
	httpx.WriteError(w, status, err.Error())
}
