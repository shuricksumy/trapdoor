package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shuricksumy/trapdoor/internal/agent"
	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/auth"
	"github.com/shuricksumy/trapdoor/internal/config"
	"github.com/shuricksumy/trapdoor/internal/history"
	"github.com/shuricksumy/trapdoor/internal/store"
)

// wgAgent is an agent API that can run WireGuard servers (file mode).
func wgAgent(t *testing.T, token string) http.Handler {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := agent.NewHost(token, log, agent.New(config.Agent{Token: token, OpenVPNDir: t.TempDir(), CCDDir: t.TempDir(), MgmtAddr: "127.0.0.1:1"}, log, nil))
	h.EnableWireGuard(config.Agent{WireGuard: "file", WireGuardDir: t.TempDir()}, nil)
	return h.Handler()
}

func TestHosts(t *testing.T) {
	ctx := context.Background()
	local := httptest.NewServer(wgAgent(t, "local-secret"))
	t.Cleanup(local.Close)
	remote := httptest.NewTLSServer(wgAgent(t, "remote-secret"))
	t.Cleanup(remote.Close)
	fp := agent.Fingerprint(remote.Certificate().Raw)

	dbPath := filepath.Join(t.TempDir(), "td.db")
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	lc := agent.NewClient(local.URL, "local-secret", 5*time.Second)
	m, err := NewMultiAgent(ctx, lc, db, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	// The certificate pin and the token are checked before saving.
	other := strings.Repeat("ab", 32)
	for name, h := range map[string]api.Host{
		"wrong pin":   {Name: "vps", URL: remote.URL, Token: "remote-secret", Fingerprint: "sha256:" + other},
		"wrong token": {Name: "vps", URL: remote.URL, Token: "nope", Fingerprint: fp},
		"plain http":  {Name: "vps", URL: local.URL, Token: "local-secret", Fingerprint: fp},
		"bad name":    {Name: "VPS.1", URL: remote.URL, Token: "remote-secret", Fingerprint: fp},
		"bad pin":     {Name: "vps", URL: remote.URL, Token: "remote-secret", Fingerprint: "abc"},
	} {
		if _, err := m.AddHost(ctx, h); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if hs := m.Hosts(ctx); len(hs) != 0 {
		t.Fatalf("hosts after failures = %+v", hs)
	}
	// openssl's colon form works too.
	colons := strings.ToUpper(strings.TrimPrefix(fp, "sha256:"))
	var b strings.Builder
	for i := 0; i < len(colons); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(colons[i : i+2])
	}
	st, err := m.AddHost(ctx, api.Host{Name: "vps", URL: remote.URL + "/", Token: "remote-secret", Fingerprint: b.String()})
	if err != nil || st.Name != "vps" || st.Version == "" {
		t.Fatalf("add = %+v, %v", st, err)
	}

	// Servers are created on the host and listed as "<host>.<id>".
	if _, err := m.CreateServer(ctx, api.CreateServer{Type: api.TypeWireGuard, ID: "wg0", PublicHost: "vpn.example.com"}); err != nil {
		t.Fatal(err)
	}
	info, err := m.CreateServer(ctx, api.CreateServer{Host: "vps", Type: api.TypeWireGuard, ID: "wg0", PublicHost: "vps.example.com"})
	if err != nil || info.ID != "vps.wg0" || info.Host != "vps" {
		t.Fatalf("create on vps = %+v, %v", info, err)
	}
	all, err := m.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range all.Servers {
		ids = append(ids, s.ID+"@"+s.Host)
	}
	if strings.Join(ids, ",") != "openvpn@,wg0@,vps.openvpn@vps,vps.wg0@vps" || len(all.Hosts) != 1 || all.Hosts[0].Error != "" {
		t.Errorf("info = %v, hosts %+v", ids, all.Hosts)
	}
	if _, ok := all.Hosts[0].Containers[api.TypeWireGuard]; !ok {
		t.Errorf("the host does not say what it runs: %+v", all.Hosts[0])
	}
	if si, err := m.Server("vps.wg0").ServerInfo(ctx); err != nil || si.Endpoint != "vps.example.com:51820" {
		t.Errorf("vps.wg0 = %+v, %v", si, err)
	}
	if _, err := m.Server("nowhere.wg0").ServerInfo(ctx); !agent.IsStatus(err, http.StatusNotFound) {
		t.Errorf("unknown host: %v", err)
	}
	if _, err := m.CreateServer(ctx, api.CreateServer{Host: "nowhere", Type: api.TypeWireGuard, ID: "wg1", PublicHost: "h"}); !agent.IsStatus(err, http.StatusNotFound) {
		t.Errorf("create on unknown host: %v", err)
	}

	// Kept in the database: a restarted web process knows the host.
	m2, err := NewMultiAgent(ctx, lc, db, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m2.Server("vps.wg0").ServerInfo(ctx); err != nil {
		t.Errorf("after restart: %v", err)
	}

	// A host that stops answering is listed with the error; the rest works.
	remote.Close()
	all, err = m.Info(ctx)
	if err != nil || len(all.Hosts) != 1 || all.Hosts[0].Error == "" || len(all.Servers) != 2 {
		t.Errorf("host down: %+v, %v", all, err)
	}

	if err := m.RemoveHost(ctx, "vps"); err != nil {
		t.Fatal(err)
	}
	if err := m.RemoveHost(ctx, "vps"); !agent.IsStatus(err, http.StatusNotFound) {
		t.Errorf("remove twice: %v", err)
	}
	if hs := m.Hosts(ctx); len(hs) != 0 {
		t.Errorf("hosts after remove = %+v", hs)
	}
}

func TestHostRoutes(t *testing.T) {
	ctx := context.Background()
	local := httptest.NewServer(wgAgent(t, "local-secret"))
	t.Cleanup(local.Close)
	remote := httptest.NewTLSServer(wgAgent(t, "remote-secret"))
	t.Cleanup(remote.Close)
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "td.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	m, err := NewMultiAgent(ctx, agent.NewClient(local.URL, "local-secret", 5*time.Second), db, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := New(Deps{Agent: m, Hosts: m, Stats: &history.Stats{DB: db}, Auth: auth.New(db, auth.Options{}),
		APIToken: "tok", Location: time.UTC, Log: log}).Handler()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	body, _ := json.Marshal(api.Host{Name: "vps", URL: remote.URL, Token: "remote-secret", Fingerprint: agent.Fingerprint(remote.Certificate().Raw)})
	if rec := call(http.MethodPost, "/api/v1/hosts", string(body)); rec.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodPost, "/api/v1/hosts", `{"name":"x","url":"https://127.0.0.1:1","token":"t","fingerprint":"sha256:`+strings.Repeat("0", 64)+`"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unreachable host: %d %s", rec.Code, rec.Body)
	}
	rec := call(http.MethodGet, "/api/v1/hosts", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"vps"`) || strings.Contains(rec.Body.String(), "remote-secret") {
		t.Errorf("list: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodPost, "/api/v1/servers", `{"host":"vps","type":"wireguard","id":"wg0","public_host":"vps.example.com"}`); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"id":"vps.wg0"`) {
		t.Errorf("create: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodGet, "/api/v1/servers/vps.wg0/peers", ""); rec.Code != http.StatusOK {
		t.Errorf("peers on vps.wg0: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodGet, "/api/v1/status", ""); !strings.Contains(rec.Body.String(), `"id":"vps.wg0"`) {
		t.Errorf("status: %s", rec.Body)
	}
	if rec := call(http.MethodDelete, "/api/v1/hosts/vps", ""); rec.Code != http.StatusNoContent {
		t.Errorf("remove: %d %s", rec.Code, rec.Body)
	}
}
