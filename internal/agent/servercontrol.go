package agent

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/httpx"
)

// Starting and stopping servers. The state is two marker files next to the
// server, read by the agent and by the VPN container's entrypoint alike:
//
//	stopped      the server does not run (until started again)
//	noautostart  the server stays stopped after the VPN container (or the
//	             agent, direct mode) starts: the entrypoint creates
//	             "stopped" for it at start
//
// OpenVPN: trapdoor.stopped / trapdoor.noautostart in its directory.
// WireGuard: <id>.stopped / <id>.noautostart next to <id>.conf.

func markerSet(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func setMarker(path string, on bool) error {
	if on {
		return os.WriteFile(path, []byte("trapdoor\n"), 0o644)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// controlRoutes adds start, stop and autostart to a server's API.
func controlRoutes(mux *http.ServeMux, start, stop func(context.Context) (string, error), autostart func(bool) error, info func(context.Context) api.ServerInfo) {
	mux.HandleFunc("POST /start", func(w http.ResponseWriter, r *http.Request) {
		note, err := start(r.Context())
		if err != nil {
			httpx.WriteError(w, http.StatusBadGateway, err.Error())
			return
		}
		i := info(r.Context())
		i.Note = note
		httpx.WriteJSON(w, http.StatusOK, i)
	})
	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, r *http.Request) {
		note, err := stop(r.Context())
		if err != nil {
			httpx.WriteError(w, http.StatusBadGateway, err.Error())
			return
		}
		i := info(r.Context())
		i.Note = note
		httpx.WriteJSON(w, http.StatusOK, i)
	})
	mux.HandleFunc("PUT /autostart", func(w http.ResponseWriter, r *http.Request) {
		var req api.Autostart
		if !decodeBody(w, r, &req) {
			return
		}
		if err := autostart(req.Autostart); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, info(r.Context()))
	})
}

// --- OpenVPN ---

func (s *Server) marker(name string) string { return filepath.Join(s.cfg.OpenVPNDir, "trapdoor."+name) }

// stopped reports whether the server was stopped in trapdoor.
func (s *Server) stopped() bool { return markerSet(s.marker("stopped")) }

func (s *Server) autostart() bool { return !markerSet(s.marker("noautostart")) }

// errStopped answers calls that need a running OpenVPN.
var errStopped = errors.New("the server is stopped; start it first")

func (s *Server) stop(ctx context.Context) (string, error) {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	if err := setMarker(s.marker("stopped"), true); err != nil {
		return "", err
	}
	// The container's supervisor sees the marker and does not start it
	// again; a host install (systemd) exits on SIGTERM too.
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.mgmt.Signal(sctx, "SIGTERM"); err != nil {
		s.log.Info("stop: OpenVPN was not running", "err", err)
	}
	s.log.Info("server stopped")
	return "", nil
}

func (s *Server) start(ctx context.Context) (string, error) {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	if err := setMarker(s.marker("stopped"), false); err != nil {
		return "", err
	}
	s.log.Info("server started")
	if _, err := os.Stat(s.conf.Path); err != nil {
		return "Not set up yet: run its setup.", nil
	}
	// Under the Docker image the VPN container starts it within seconds.
	wctx, cancel := context.WithTimeout(ctx, s.setupWait())
	defer cancel()
	if _, err := s.waitForOpenVPN(wctx, s.setupWait()); err != nil {
		return "OpenVPN does not answer yet. Without the Docker image, start it yourself (e.g. systemctl start openvpn-server@…).", nil
	}
	return "", nil
}

func (s *Server) setAutostart(on bool) error { return setMarker(s.marker("noautostart"), !on) }

func (s *Server) registerControlRoutes(mux *http.ServeMux) {
	controlRoutes(mux, s.start, s.stop, s.setAutostart, s.Info)
}

// --- WireGuard / AmneziaWG ---

func (s *WGServer) marker(name string) string { return wgPath(s.dir, s.st.ID, "."+name) }

func (s *WGServer) stopped() bool { return markerSet(s.marker("stopped")) }

func (s *WGServer) autostart() bool { return !markerSet(s.marker("noautostart")) }

func (s *WGServer) stop(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := setMarker(s.marker("stopped"), true); err != nil {
		return "", err
	}
	// file mode: the VPN container takes it down; direct: here.
	if err := s.run.Down(ctx, s.st.ID, s.confPath(), s.subnet()); err != nil {
		return "", err
	}
	s.sess = map[string]*wgSession{} // counters start again with the interface
	s.applied = netip.Prefix{}
	s.log.Info("server stopped")
	return "", nil
}

func (s *WGServer) start(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := setMarker(s.marker("stopped"), false); err != nil {
		return "", err
	}
	s.applyLocked(ctx, false)
	s.log.Info("server started")
	if s.applyErr != "" {
		return "", errors.New(s.applyErr)
	}
	return "", nil
}

func (s *WGServer) setAutostart(on bool) error { return setMarker(s.marker("noautostart"), !on) }
