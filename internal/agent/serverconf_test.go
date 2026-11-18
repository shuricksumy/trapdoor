package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/config"
	"github.com/shuricksumy/trapdoor/internal/mgmt"
)

// restartingMgmt simulates OpenVPN: after a signal the old process keeps
// answering for a while (explicit-exit-notify), then it is down until it
// "starts" again, which only works if server.conf has no "broken-on-start".
type restartingMgmt struct {
	fakeMgmt
	mu      sync.Mutex
	conf    string
	exiting time.Time // the old process answers until then
	down    time.Time // down until
}

func (m *restartingMgmt) Signal(context.Context, string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exiting = time.Now().Add(2500 * time.Millisecond)
	m.down = m.exiting.Add(500 * time.Millisecond)
	return nil
}

func (m *restartingMgmt) Version(context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if time.Now().Before(m.exiting) {
		return "OpenVPN 2.6.19", nil // still the old one
	}
	if time.Now().Before(m.down) {
		return "", fmt.Errorf("%w: down", mgmt.ErrUnavailable)
	}
	data, _ := os.ReadFile(m.conf)
	if strings.Contains(string(data), "broken-on-start") {
		return "", fmt.Errorf("%w: crashed", mgmt.ErrUnavailable)
	}
	return "OpenVPN 2.6.19", nil
}

const goodConf = "port 1194\nproto udp\ndev tun\nserver 10.8.0.0 255.255.255.0\nca ca.crt\ncert server.crt\nkey server.key\nmanagement 127.0.0.1 2080\ncrl-verify crl.pem\nclient-config-dir ccd\n"

func confServer(t *testing.T) (*httptest.Server, *restartingMgmt, string) {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{"ca.crt", "server.crt", "server.key", "crl.pem"} {
		_ = os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600)
	}
	_ = os.Mkdir(filepath.Join(dir, "ccd"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "server.conf"), []byte(goodConf), 0o640)
	bin := "openvpn"
	if _, err := os.Stat("/usr/sbin/openvpn"); err != nil {
		bin = "" // no binary: skip OpenVPN's own parse check
	}
	cfg := config.Agent{Token: "secret", OpenVPNDir: dir, CCDDir: filepath.Join(dir, "ccd"), OpenVPNBin: bin, RestartSignal: "SIGTERM"}
	s := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	m := &restartingMgmt{conf: filepath.Join(dir, "server.conf")}
	s.mgmt = m
	s.restartWait = 6 * time.Second
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, m, dir
}

func call(t *testing.T, srv *httptest.Server, method, path string, body any, out any) int {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, srv.URL+path, r)
	req.Header.Set("X-Trapdoor-Token", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func TestServerConfigEditAndRestart(t *testing.T) {
	srv, _, dir := confServer(t)

	var cfg api.ServerConfig
	if code := call(t, srv, "GET", "/v1/server/config", nil, &cfg); code != 200 || cfg.Settings.Port != 1194 || cfg.Pending || cfg.Restart != "SIGTERM" {
		t.Fatalf("get = %d %+v", code, cfg)
	}
	for _, p := range cfg.Problems {
		if p.Error {
			t.Fatalf("good config has an error: %s", p.Message)
		}
	}

	// Save settings: port 443, a DNS server.
	st := cfg.Settings
	st.Port, st.DNS = 443, []string{"10.8.0.1"}
	var saved api.ServerConfig
	if code := call(t, srv, "PUT", "/v1/server/config", api.ServerConfigUpdate{BaseVersion: cfg.Version, Settings: &st}, &saved); code != 200 {
		t.Fatalf("save = %d", code)
	}
	if saved.Settings.Port != 443 || !saved.Pending || !strings.Contains(saved.Raw, `push "dhcp-option DNS 10.8.0.1"`) {
		t.Errorf("saved = %+v", saved)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "server.conf")); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode changed to %v", fi.Mode().Perm())
	}
	var backups []api.ConfigBackup
	if call(t, srv, "GET", "/v1/server/backups", nil, &backups); len(backups) != 1 {
		t.Fatalf("backups = %+v", backups)
	}
	var bk struct{ Raw string }
	if call(t, srv, "GET", "/v1/server/backups/"+backups[0].Name, nil, &bk); bk.Raw != goodConf {
		t.Errorf("backup content = %q", bk.Raw)
	}
	if code := call(t, srv, "GET", "/v1/server/backups/../server.conf", nil, nil); code != 404 && code != 400 && code != 301 {
		t.Errorf("path traversal: %d", code)
	}

	// Stale version → 409.
	if code := call(t, srv, "PUT", "/v1/server/config", api.ServerConfigUpdate{BaseVersion: cfg.Version, Settings: &st}, nil); code != 409 {
		t.Errorf("stale save = %d, want 409", code)
	}
	// Invalid form values → 400; config with errors → 422 and nothing written.
	bad := st
	bad.Subnet = "10.8.0.0/31"
	if code := call(t, srv, "PUT", "/v1/server/config", api.ServerConfigUpdate{BaseVersion: saved.Version, Settings: &bad}, nil); code != 400 {
		t.Errorf("bad settings = %d", code)
	}
	noKey := strings.Replace(saved.Raw, "key server.key", "key missing.key", 1)
	var rejected struct{ Problems []api.ConfigProblem }
	if code := call(t, srv, "PUT", "/v1/server/config", api.ServerConfigUpdate{BaseVersion: saved.Version, Raw: &noKey}, &rejected); code != 422 || len(rejected.Problems) == 0 {
		t.Errorf("config with missing key = %d %+v", code, rejected)
	}

	// Check (dry run) does not write.
	var chk api.ConfigCheck
	call(t, srv, "POST", "/v1/server/config/check", api.ServerConfigUpdate{Raw: &noKey}, &chk)
	if len(chk.Problems) == 0 {
		t.Error("check found nothing")
	}

	// Restart: comes back, pending cleared.
	var res api.RestartResult
	if call(t, srv, "POST", "/v1/server/restart", api.RestartRequest{Rollback: true}, &res); !res.OK || res.OpenVPNVersion == "" {
		t.Fatalf("restart = %+v", res)
	}
	call(t, srv, "GET", "/v1/server/config", nil, &cfg)
	if cfg.Pending {
		t.Error("still pending after restart")
	}
}

func TestRestartRollsBack(t *testing.T) {
	srv, _, dir := confServer(t)
	var cfg api.ServerConfig
	call(t, srv, "GET", "/v1/server/config", nil, &cfg)
	// A config that passes the checks but keeps "OpenVPN" from starting.
	raw := cfg.Raw + "# broken-on-start\n"
	var saved api.ServerConfig
	if code := call(t, srv, "PUT", "/v1/server/config", api.ServerConfigUpdate{BaseVersion: cfg.Version, Raw: &raw}, &saved); code != 200 {
		t.Fatalf("save = %d", code)
	}
	var res api.RestartResult
	call(t, srv, "POST", "/v1/server/restart", api.RestartRequest{Rollback: true}, &res)
	if res.OK || !res.RolledBack || !strings.Contains(res.Error, "running again") {
		t.Fatalf("restart = %+v", res)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "server.conf"))
	if string(data) != goodConf {
		t.Errorf("not rolled back:\n%s", data)
	}
}
