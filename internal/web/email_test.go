package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shuricksumy/trapdoor/internal/agent"
	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/auth"
	"github.com/shuricksumy/trapdoor/internal/history"
	"github.com/shuricksumy/trapdoor/internal/mail"
	"github.com/shuricksumy/trapdoor/internal/store"
)

// outbox records the emails a test server sends.
type outbox struct {
	mu   sync.Mutex
	sent []mail.Message
	fail error
}

func (o *outbox) send(_ context.Context, c mail.Config, m mail.Message) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if c.Host == "" {
		return errors.New("no host")
	}
	if o.fail != nil {
		return o.fail
	}
	o.sent = append(o.sent, m)
	return nil
}

func (o *outbox) take() []mail.Message {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := o.sent
	o.sent = nil
	return out
}

// fakeHosts is a HostManager whose nodes answer or not.
type fakeHosts struct{ hosts []api.HostStatus }

func (f *fakeHosts) Hosts(context.Context) []api.HostStatus { return f.hosts }
func (f *fakeHosts) AddHost(context.Context, api.Host) (api.HostStatus, error) {
	return api.HostStatus{}, errors.New("no")
}
func (f *fakeHosts) RemoveHost(context.Context, string) error { return nil }
func (f *fakeHosts) HostFingerprint(string) (string, bool)    { return "", false }

func TestEmail(t *testing.T) {
	ctx := context.Background()
	local := httptest.NewServer(wgAgent(t, "local-secret"))
	t.Cleanup(local.Close)
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "td.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	m, err := NewMultiAgent(ctx, agent.NewClient(local.URL, "local-secret", 5*time.Second), db, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreateServer(ctx, api.CreateServer{Type: api.TypeWireGuard, ID: "wg0", Name: "Home", PublicHost: "vpn.example.com"}); err != nil {
		t.Fatal(err)
	}
	box := &outbox{}
	var mu sync.Mutex
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	nodes := &fakeHosts{}
	srv := New(Deps{Agent: m, Hosts: nodes, Backup: m, Stats: &history.Stats{DB: db}, Auth: auth.New(db, auth.Options{}),
		APIToken: "tok", Location: time.UTC, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: clock,
		SendMail: box.send, PublicURL: "https://vpn-admin.example.com"})
	h := srv.Handler()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// Not set up: test and send refuse, notifications are quiet.
	if rec := call(http.MethodPost, "/api/v1/email/test", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("test without setup: %d", rec.Code)
	}
	srv.notify(ctx, func(Notify) bool { return true }, "x", "y")
	srv.mailWG.Wait()
	if n := len(box.take()); n != 0 {
		t.Errorf("sent %d without setup", n)
	}

	for _, bad := range []string{
		`{"enabled":true,"host":"smtp.example.com","port":0,"security":"starttls","from":"vpn@example.com"}`,
		`{"enabled":true,"host":"smtp.example.com","port":587,"security":"ssl","from":"vpn@example.com"}`,
		`{"enabled":true,"host":"smtp.example.com","port":587,"security":"starttls","from":"nobody"}`,
		`{"enabled":true,"host":"smtp.example.com","port":587,"security":"starttls","from":"vpn@example.com","admins":["not an address"]}`,
	} {
		if rec := call(http.MethodPut, "/api/v1/email", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	good := `{"enabled":true,"host":"smtp.example.com","port":587,"security":"starttls","username":"vpn","password":"smtp-secret","from":"trapdoor <vpn@example.com>","admins":["Admin <admin@example.com>"],"notify":{"backup_failed":true,"node_down":true,"cert_expiry":true}}`
	if rec := call(http.MethodPut, "/api/v1/email", good); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "smtp-secret") {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	// The password is kept when the form sends none, and never shown.
	if rec := call(http.MethodPut, "/api/v1/email", strings.Replace(good, `"password":"smtp-secret",`, "", 1)); rec.Code != http.StatusOK {
		t.Fatalf("save again: %d", rec.Code)
	}
	rec := call(http.MethodGet, "/api/v1/email", "")
	var got EmailSettings
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if strings.Contains(rec.Body.String(), "smtp-secret") || !got.PasswordSet || got.Admins[0] != "admin@example.com" {
		t.Fatalf("get: %s", rec.Body)
	}
	if es, _ := srv.emailSettings(ctx); es.Password != "smtp-secret" {
		t.Fatal("password lost")
	}

	if rec := call(http.MethodPost, "/api/v1/email/test", ""); rec.Code != http.StatusOK {
		t.Fatalf("test: %d %s", rec.Code, rec.Body)
	}
	if sent := box.take(); len(sent) != 1 || sent[0].To[0] != "admin@example.com" || !strings.Contains(sent[0].Text, "vpn-admin.example.com") {
		t.Fatalf("test email: %+v", sent)
	}
	box.fail = errors.New("535 bad login")
	if rec := call(http.MethodPost, "/api/v1/email/test", `{"to":"other@example.com"}`); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "535") {
		t.Errorf("failing test: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodGet, "/api/v1/email", ""); !strings.Contains(rec.Body.String(), `"ok":false`) {
		t.Errorf("last error not shown: %s", rec.Body)
	}
	box.fail = nil

	// A client's address, and its profile by email.
	if rec := call(http.MethodPost, "/api/v1/servers/wg0/peers", `{"name":"phone"}`); rec.Code != http.StatusCreated {
		t.Fatalf("peer: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodPost, "/api/v1/servers/wg0/emails/phone/send", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("send without an address: %d", rec.Code)
	}
	if rec := call(http.MethodPut, "/api/v1/servers/wg0/emails/phone", `{"email":"bad"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad address: %d", rec.Code)
	}
	if rec := call(http.MethodPut, "/api/v1/servers/wg0/emails/phone", `{"email":"Ann <ann@example.com>"}`); rec.Code != http.StatusOK {
		t.Fatalf("set address: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodGet, "/api/v1/servers/wg0/emails", ""); !strings.Contains(rec.Body.String(), `"phone":"ann@example.com"`) {
		t.Errorf("addresses: %s", rec.Body)
	}
	if rec := call(http.MethodPost, "/api/v1/servers/wg0/emails/phone/send", `{"qr_gif":"R0lGODlhAQABAAAAACw=","note":"Ask me if it does not work."}`); rec.Code != http.StatusOK {
		t.Fatalf("send: %d %s", rec.Code, rec.Body)
	}
	sent := box.take()
	if len(sent) != 1 || sent[0].To[0] != "ann@example.com" || len(sent[0].Attachments) != 2 ||
		sent[0].Attachments[0].Name != "phone.conf" || !strings.Contains(string(sent[0].Attachments[0].Data), "[Interface]") ||
		sent[0].Attachments[1].Name != "phone-qr.gif" || !strings.Contains(sent[0].Text, "Ask me") || !strings.Contains(sent[0].Subject, "Home") {
		t.Fatalf("profile email: %+v", sent)
	}
	// Another address: sent there and stored.
	if rec := call(http.MethodPost, "/api/v1/servers/wg0/emails/phone/send", `{"email":"ann2@example.com"}`); rec.Code != http.StatusOK {
		t.Fatalf("send to another address: %d", rec.Code)
	}
	if rec := call(http.MethodGet, "/api/v1/servers/wg0/emails", ""); !strings.Contains(rec.Body.String(), "ann2@example.com") {
		t.Errorf("new address not stored: %s", rec.Body)
	}
	box.take()
	// Removing the client forgets its address.
	if rec := call(http.MethodDelete, "/api/v1/servers/wg0/peers/phone", ""); rec.Code/100 != 2 {
		t.Fatalf("remove: %d", rec.Code)
	}
	if rec := call(http.MethodGet, "/api/v1/servers/wg0/emails", ""); strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Errorf("address kept after remove: %s", rec.Body)
	}

	// Node down: an email after 5 minutes, one more when it is back.
	nodes.hosts = []api.HostStatus{{Name: "vps", URL: "https://203.0.113.5:9443", Error: "connection refused"}}
	step := func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
		srv.checkNodes(ctx)
		srv.mailWG.Wait()
	}
	step(0)
	step(4 * time.Minute)
	if n := len(box.take()); n != 0 {
		t.Errorf("node email after 4 minutes: %d", n)
	}
	step(time.Minute)
	step(time.Minute)
	if sent := box.take(); len(sent) != 1 || !strings.Contains(sent[0].Subject, "vps is not reachable") {
		t.Fatalf("node down: %+v", sent)
	}
	nodes.hosts[0].Error = ""
	step(time.Minute)
	if sent := box.take(); len(sent) != 1 || !strings.Contains(sent[0].Subject, "vps is back") {
		t.Fatalf("node back: %+v", sent)
	}

	// Expiring certificates: each one once.
	soon := now.Add(10 * 24 * time.Hour)
	srv.notifyExpiry(ctx, []expiringCert{{server: "OpenVPN", name: "laptop", notAfter: soon}})
	srv.mailWG.Wait()
	srv.notifyExpiry(ctx, []expiringCert{{server: "OpenVPN", name: "laptop", notAfter: soon}})
	srv.mailWG.Wait()
	if sent := box.take(); len(sent) != 1 || !strings.Contains(sent[0].Text, "laptop on OpenVPN") {
		t.Fatalf("expiry: %+v", sent)
	}
	srv.notifyExpiry(ctx, []expiringCert{{server: "OpenVPN", name: "laptop", notAfter: soon}, {server: "OpenVPN", name: "phone", notAfter: soon}})
	srv.mailWG.Wait()
	if sent := box.take(); len(sent) != 1 || strings.Contains(sent[0].Text, "laptop") || !strings.Contains(sent[0].Text, "phone") {
		t.Fatalf("second expiry: %+v", sent)
	}

	// Off keeps the settings but sends nothing.
	if rec := call(http.MethodPut, "/api/v1/email", strings.Replace(good, `"enabled":true`, `"enabled":false`, 1)); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	if es, _ := srv.emailSettings(ctx); es.Host != "smtp.example.com" || es.Password != "smtp-secret" {
		t.Errorf("settings lost when turned off: %+v", es)
	}
	if rec := call(http.MethodPost, "/api/v1/email/test", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("test while off: %d", rec.Code)
	}

	// Turned off per kind.
	if rec := call(http.MethodPut, "/api/v1/email", strings.Replace(good, `"node_down":true`, `"node_down":false`, 1)); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	nodes.hosts[0].Error = "down again"
	step(0)
	step(10 * time.Minute)
	if n := len(box.take()); n != 0 {
		t.Errorf("node email although turned off: %d", n)
	}

	// A failed scheduled backup is emailed.
	srv.BackupDir = t.TempDir()
	local.Close()
	_, _ = srv.runBackup(ctx, defaultSchedule, false)
	srv.mailWG.Wait()
	if sent := box.take(); len(sent) != 1 || !strings.Contains(sent[0].Subject, "backup failed") {
		t.Fatalf("backup failure: %+v", sent)
	}
}
