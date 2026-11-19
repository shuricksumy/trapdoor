package web

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/history"
	"github.com/shuricksumy/trapdoor/internal/httpx"
)

func (s *Server) registerServerControlRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/servers", s.handleServers)
	mux.HandleFunc("POST /api/v1/servers/{server}/start", s.handleStartServer)
	mux.HandleFunc("POST /api/v1/servers/{server}/stop", s.handleStopServer)
	mux.HandleFunc("PUT /api/v1/servers/{server}/autostart", s.handleAutostart)
}

func (s *Server) handleStartServer(w http.ResponseWriter, r *http.Request) {
	info, err := s.agent(r).StartServer(r.Context())
	if err != nil {
		s.agentError(w, err)
		return
	}
	s.audit(r, "server.start", r.PathValue("server"))
	httpx.WriteJSON(w, http.StatusOK, info)
}

func (s *Server) handleStopServer(w http.ResponseWriter, r *http.Request) {
	info, err := s.agent(r).StopServer(r.Context())
	if err != nil {
		s.agentError(w, err)
		return
	}
	s.audit(r, "server.stop", r.PathValue("server"))
	httpx.WriteJSON(w, http.StatusOK, info)
}

func (s *Server) handleAutostart(w http.ResponseWriter, r *http.Request) {
	var req api.Autostart
	if !decodeJSON(w, r, &req) {
		return
	}
	info, err := s.agent(r).SetAutostart(r.Context(), req.Autostart)
	if err != nil {
		s.agentError(w, err)
		return
	}
	s.audit(r, "server.autostart", r.PathValue("server"))
	httpx.WriteJSON(w, http.StatusOK, info)
}

// handleServers lists every server on every machine with its numbers, for
// the Servers page: one call instead of one per server.
func (s *Server) handleServers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	info, err := s.Agent.Info(ctx)
	if err != nil {
		s.agentError(w, err)
		return
	}
	today := midnightIn(s.Now(), s.Location).Unix()
	traffic, err := s.Stats.DB.TrafficSince(ctx, today)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]api.ServerSummary, len(info.Servers))
	var wg sync.WaitGroup
	for i, si := range info.Servers {
		out[i] = api.ServerSummary{ServerInfo: si, TodayIn: traffic[si.ID][0], TodayOut: traffic[si.ID][1]}
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, hostInfoTimeout)
			defer cancel()
			s.summarize(cctx, &out[i])
		}()
	}
	wg.Wait()
	httpx.WriteJSON(w, http.StatusOK, out)
}

// summarize fills in the client counts of one server.
func (s *Server) summarize(ctx context.Context, sum *api.ServerSummary) {
	a := s.Agent.Server(sum.ID)
	switch sum.Type {
	case api.TypeOpenVPN:
		if sum.PKIReady {
			certs, err := a.ListCerts(ctx)
			if err != nil {
				sum.SummaryError = err.Error()
				return
			}
			now := s.Now()
			for _, c := range certs {
				if c.Kind != "server" && c.Current && c.Status == "valid" && now.Before(c.NotAfter) {
					sum.Clients++
				}
			}
		}
	default:
		peers, err := a.Peers(ctx)
		if err != nil {
			sum.SummaryError = err.Error()
			return
		}
		for _, p := range peers {
			if !p.Disabled {
				sum.Clients++
			}
		}
	}
	if sum.Stopped || (sum.Type == api.TypeOpenVPN && !sum.PKIReady) {
		return // nothing runs that could have sessions
	}
	if live, err := a.Sessions(ctx); err == nil {
		sum.Connected = len(live.Sessions)
	} else if sum.SummaryError == "" {
		sum.SummaryError = err.Error()
	}
}

// midnightIn is the start of t's day in loc.
func midnightIn(t time.Time, loc *time.Location) time.Time {
	from, _, err := history.Preset("today", t, loc)
	if err != nil {
		y, m, d := t.In(loc).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, loc)
	}
	return from
}
