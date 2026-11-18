package agent

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/shuricksumy/tunnelward/internal/api"
	"github.com/shuricksumy/tunnelward/internal/httpx"
	"github.com/shuricksumy/tunnelward/internal/serverconf"
)

func (s *Server) registerServerConfigRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/server/config", s.handleGetServerConfig)
	mux.HandleFunc("POST /v1/server/config/check", s.handleCheckServerConfig)
	mux.HandleFunc("PUT /v1/server/config", s.handleSaveServerConfig)
	mux.HandleFunc("POST /v1/server/restart", s.handleRestart)
	mux.HandleFunc("GET /v1/server/backups", s.handleBackups)
	mux.HandleFunc("GET /v1/server/backups/{name}", s.handleGetBackup)
}

func toSettings(x serverconf.Settings) api.ServerSettings { return api.ServerSettings(x) }
func fromSettings(x api.ServerSettings) serverconf.Settings {
	s := serverconf.Settings(x)
	for _, l := range []*[]string{&s.DNS, &s.Routes, &s.PushRoutes} {
		if *l == nil {
			*l = []string{}
		}
	}
	return s
}

func toProblems(ps []serverconf.Problem) []api.ConfigProblem {
	out := make([]api.ConfigProblem, 0, len(ps))
	for _, p := range ps {
		out = append(out, api.ConfigProblem{Error: p.Error, Message: p.Message})
	}
	return out
}

func (s *Server) handleGetServerConfig(w http.ResponseWriter, r *http.Request) {
	raw, ver, err := s.conf.Read()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, api.ServerConfig{
		Raw: raw, Version: ver,
		Settings: toSettings(serverconf.Parse(raw)),
		Pending:  s.conf.Pending(),
		Problems: toProblems(serverconf.Check(r.Context(), raw, s.cfg.OpenVPNDir, s.cfg.OpenVPNBin)),
		Restart:  s.cfg.RestartSignal,
	})
}

// candidate builds the new file from an update (raw text or settings).
func (s *Server) candidate(u api.ServerConfigUpdate) (string, error) {
	switch {
	case u.Raw != nil && u.Settings != nil:
		return "", errors.New("send raw or settings, not both")
	case u.Raw != nil:
		return *u.Raw, nil
	case u.Settings != nil:
		st := fromSettings(*u.Settings)
		if err := serverconf.Validate(st); err != nil {
			return "", err
		}
		cur, _, err := s.conf.Read()
		if err != nil {
			return "", err
		}
		return serverconf.Apply(cur, st), nil
	default:
		return "", errors.New("send raw or settings")
	}
}

func (s *Server) handleCheckServerConfig(w http.ResponseWriter, r *http.Request) {
	var u api.ServerConfigUpdate
	if !decodeBody(w, r, &u) {
		return
	}
	raw, err := s.candidate(u)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, api.ConfigCheck{
		Raw:      raw,
		Problems: toProblems(serverconf.Check(r.Context(), raw, s.cfg.OpenVPNDir, s.cfg.OpenVPNBin)),
	})
}

func (s *Server) handleSaveServerConfig(w http.ResponseWriter, r *http.Request) {
	var u api.ServerConfigUpdate
	if !decodeBody(w, r, &u) {
		return
	}
	raw, err := s.candidate(u)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	problems := serverconf.Check(r.Context(), raw, s.cfg.OpenVPNDir, s.cfg.OpenVPNBin)
	if serverconf.HasErrors(problems) {
		httpx.WriteJSON(w, http.StatusUnprocessableEntity, struct {
			Error    string              `json:"error"`
			Problems []api.ConfigProblem `json:"problems"`
		}{"the config has errors and was not saved", toProblems(problems)})
		return
	}
	if _, err := s.conf.Write(raw, u.BaseVersion); err != nil {
		if errors.Is(err, serverconf.ErrConflict) {
			httpx.WriteError(w, http.StatusConflict, err.Error())
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.Info("server.conf saved", "restart_pending", s.conf.Pending())
	s.handleGetServerConfig(w, r)
}

// handleRestart restarts OpenVPN through its management interface and waits
// until it answers again. With rollback, a config that keeps OpenVPN from
// coming back is replaced by the last one it ran with.
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	var req api.RestartRequest
	if r.ContentLength != 0 && !decodeBody(w, r, &req) {
		return
	}
	s.restartMu.Lock()
	defer s.restartMu.Unlock()

	raw, _, err := s.conf.Read()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	previous := s.conf.Running()
	start := time.Now()
	res := api.RestartResult{}

	if err := s.mgmt.Signal(r.Context(), s.cfg.RestartSignal); err != nil {
		s.mgmtError(w, err)
		return
	}
	s.log.Info("OpenVPN restart requested", "signal", s.cfg.RestartSignal)
	s.waitForExit(r.Context())
	if v, err := s.waitForOpenVPN(r.Context(), s.restartTimeout()); err == nil {
		s.conf.MarkRunning(raw)
		res.OK, res.OpenVPNVersion = true, v
		res.Seconds = time.Since(start).Seconds()
		s.log.Info("OpenVPN is back", "seconds", res.Seconds)
		httpx.WriteJSON(w, http.StatusOK, res)
		return
	}

	res.Error = "OpenVPN did not come back within " + s.restartTimeout().String()
	if req.Rollback && previous != raw && previous != "" {
		s.log.Warn("OpenVPN did not come back; restoring the previous server.conf")
		_, ver, _ := s.conf.Read()
		if _, err := s.conf.Write(previous, ver); err == nil {
			res.RolledBack = true
			// A supervised OpenVPN restarts by itself with the restored file;
			// one that is still up (SIGHUP) needs another signal.
			if s.mgmt.Signal(r.Context(), s.cfg.RestartSignal) == nil {
				s.waitForExit(r.Context())
			}
			if v, err := s.waitForOpenVPN(r.Context(), s.restartTimeout()); err == nil {
				res.OpenVPNVersion = v
				res.Error += "; the previous config was restored and OpenVPN is running again"
			} else {
				res.Error += "; the previous config was restored but OpenVPN is still not answering"
			}
		}
	}
	res.Seconds = time.Since(start).Seconds()
	httpx.WriteJSON(w, http.StatusOK, res)
}

func (s *Server) restartTimeout() time.Duration {
	if s.restartWait > 0 {
		return s.restartWait
	}
	return 30 * time.Second
}

// waitForExit waits until a SIGTERMed OpenVPN has gone (it can keep
// answering for seconds, e.g. while explicit-exit-notify tells UDP clients),
// so the old process is not taken for the restarted one.
func (s *Server) waitForExit(ctx context.Context) {
	if s.cfg.RestartSignal != "SIGTERM" {
		return
	}
	deadline := time.Now().Add(min(15*time.Second, s.restartTimeout()))
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if _, err := s.mgmt.Version(ctx); err != nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// waitForOpenVPN gives OpenVPN a moment to go down, then polls the
// management interface until it answers.
func (s *Server) waitForOpenVPN(ctx context.Context, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	time.Sleep(min(2*time.Second, timeout/4))
	for {
		v, err := s.mgmt.Version(ctx)
		if err == nil {
			return v, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return "", err
		}
		time.Sleep(time.Second)
	}
}

func (s *Server) handleBackups(w http.ResponseWriter, _ *http.Request) {
	list, err := s.conf.Backups()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]api.ConfigBackup, 0, len(list))
	for _, b := range list {
		out = append(out, api.ConfigBackup(b))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetBackup(w http.ResponseWriter, r *http.Request) {
	raw, err := s.conf.ReadBackup(r.PathValue("name"))
	if err != nil {
		if errors.Is(err, serverconf.ErrInvalid) {
			httpx.WriteError(w, http.StatusNotFound, err.Error())
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, struct {
		Raw string `json:"raw"`
	}{raw})
}
