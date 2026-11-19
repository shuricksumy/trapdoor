package web

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shuricksumy/trapdoor/internal/agent"
	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/auth"
	"github.com/shuricksumy/trapdoor/internal/backup"
	"github.com/shuricksumy/trapdoor/internal/config"
	"github.com/shuricksumy/trapdoor/internal/history"
	"github.com/shuricksumy/trapdoor/internal/store"
)

// backupAgent is an agent with an OpenVPN directory and a WireGuard server.
func backupAgent(t *testing.T, token, marker string) http.Handler {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ovpn := t.TempDir()
	for name, content := range map[string]string{
		"server.conf":                 "# " + marker + "\nport 1194\n",
		"easy-rsa/pki/private/ca.key": "CA KEY " + marker,
		"ccd/alice":                   "#2FA_KEY:00\n",
		"openvpn-status.log":          "runtime state",
	} {
		p := filepath.Join(ovpn, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Agent{Token: token, OpenVPNDir: ovpn, PKIDir: filepath.Join(ovpn, "easy-rsa/pki"), CCDDir: filepath.Join(ovpn, "ccd"),
		OpenVPNServersDir: filepath.Join(ovpn, "servers"), MgmtAddr: "127.0.0.1:1", WireGuard: "file", WireGuardDir: t.TempDir(), AmneziaWGDir: t.TempDir()}
	h := agent.NewHost(token, log, agent.New(cfg, log, nil))
	h.EnableWireGuard(cfg, nil)
	h.EnableOpenVPN(cfg, nil)
	return h.Handler()
}

func readBackup(t *testing.T, data []byte, pass string) map[string]string {
	t.Helper()
	var r io.Reader = bytes.NewReader(data)
	if pass != "" {
		var err error
		if r, err = backup.NewDecrypter(r, pass); err != nil {
			t.Fatal(err)
		}
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = string(b)
	}
}

func TestBackup(t *testing.T) {
	ctx := context.Background()
	local := httptest.NewServer(backupAgent(t, "local-secret", "local"))
	t.Cleanup(local.Close)
	remote := httptest.NewTLSServer(backupAgent(t, "remote-secret", "remote"))
	t.Cleanup(remote.Close)
	down := httptest.NewTLSServer(backupAgent(t, "down-secret", "down"))
	t.Cleanup(down.Close)

	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "td.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	lc := agent.NewClient(local.URL, "local-secret", 5*time.Second)
	m, err := NewMultiAgent(ctx, lc, db, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for name, srv := range map[string]*httptest.Server{"vps": remote, "office": down} {
		tok := map[string]string{"vps": "remote-secret", "office": "down-secret"}[name]
		if _, err := m.AddHost(ctx, api.Host{Name: name, URL: srv.URL, Token: tok, Fingerprint: agent.Fingerprint(srv.Certificate().Raw)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.CreateServer(ctx, api.CreateServer{Type: api.TypeWireGuard, ID: "wg0", PublicHost: "vpn.example.com"}); err != nil {
		t.Fatal(err)
	}
	down.Close() // one host is unreachable when the backup runs

	authSvc := auth.New(db, auth.Options{})
	if _, err := authSvc.CreateUser(ctx, "admin", "a-long-password"); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := New(Deps{Agent: m, Hosts: m, Backup: m, Stats: &history.Stats{DB: db}, Auth: authSvc,
		APIToken: "tok", Location: time.UTC, Log: log}).Handler()
	get := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/backup", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := get(`{"passphrase":"backup pass"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Disposition"), ".tdbackup") {
		t.Fatalf("backup: %d %s", rec.Code, rec.Header())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("CA KEY")) {
		t.Fatal("encrypted backup contains the CA key in plain text")
	}
	files := readBackup(t, rec.Body.Bytes(), "backup pass")
	for name, want := range map[string]string{
		"agent/openvpn/server.conf":                     "# local",
		"agent/openvpn/easy-rsa/pki/private/ca.key":     "CA KEY local",
		"agent/openvpn/ccd/alice":                       "#2FA_KEY",
		"agent/wireguard/wg0.json":                      `"id": "wg0"`,
		"agent/wireguard/wg0.conf":                      "[Interface]",
		"hosts/vps/openvpn/server.conf":                 "# remote",
		"hosts/vps/openvpn/easy-rsa/pki/private/ca.key": "CA KEY remote",
		"hosts/office.error":                            "",
		"web/trapdoor.db":                               "SQLite format 3",
	} {
		got, ok := files[name]
		if !ok || !strings.Contains(got, want) {
			t.Errorf("%s: %q (present %v)", name, got[:min(len(got), 60)], ok)
		}
	}
	if _, ok := files["agent/openvpn/openvpn-status.log"]; ok {
		t.Error("runtime state in the backup")
	}
	var man backup.Manifest
	if err := json.Unmarshal([]byte(files["manifest.json"]), &man); err != nil || man.Format != 1 ||
		strings.Join(man.Parts, ",") != "agent,web,hosts/office,hosts/vps" {
		t.Errorf("manifest = %+v, %v", man, err)
	}
	// The database copy is a working database with the admin in it.
	dbCopy := filepath.Join(t.TempDir(), "copy.db")
	if err := os.WriteFile(dbCopy, []byte(files["web/trapdoor.db"]), 0o600); err != nil {
		t.Fatal(err)
	}
	cdb, err := store.Open(ctx, dbCopy)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cdb.Close() }()
	if n, err := auth.New(cdb, auth.Options{}).CountUsers(ctx); err != nil || n != 1 {
		t.Errorf("users in the copy: %d, %v", n, err)
	}
	if hs, _ := cdb.Hosts(ctx); len(hs) != 2 {
		t.Errorf("hosts in the copy: %+v", hs)
	}
	// No snapshot files are left behind next to the database.
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(dbCopy), ".snapshot-*")); len(left) != 0 {
		t.Errorf("snapshots left: %v", left)
	}

	// Without a passphrase: a plain .tar.gz.
	rec = get(`{}`)
	if rec.Code != http.StatusOK || !strings.HasSuffix(strings.Trim(rec.Header().Get("Content-Disposition"), `"`), ".tar.gz") {
		t.Fatalf("plain backup: %d %s", rec.Code, rec.Header())
	}
	if files := readBackup(t, rec.Body.Bytes(), ""); files["agent/openvpn/server.conf"] == "" {
		t.Error("plain backup lacks the OpenVPN directory")
	}
}
