package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shuricksumy/trapdoor/internal/auth"
	"github.com/shuricksumy/trapdoor/internal/history"
	"github.com/shuricksumy/trapdoor/internal/mail"
	"github.com/shuricksumy/trapdoor/internal/store"
)

func TestPresence(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "td.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mails, tg := &outbox{}, &tgbox{}
	var mu sync.Mutex
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	at := func(d time.Duration) {
		mu.Lock()
		now = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC).Add(d)
		mu.Unlock()
	}
	srv := New(Deps{Stats: &history.Stats{DB: db}, Auth: auth.New(db, auth.Options{}), APIToken: "tok", Location: time.UTC,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: clock, SendMail: mails.send, SendTelegram: tg.send})
	h := srv.Handler()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// Channels: email and a bot; changes go to their own address and chat.
	if err := db.SetSetting(ctx, settingEmail, EmailSettings{Enabled: true, Config: mail.Config{Host: "smtp.example.com", Port: 587, From: "vpn@example.com"},
		Admins: []string{"admin@example.com"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, settingTelegram, TelegramSettings{Enabled: true, Token: tgToken, Chats: []string{"4242"}}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"grace_minutes":0}`, `{"grace_minutes":2,"emails":["nope"]}`, `{"grace_minutes":2,"chats":["x y"]}`} {
		if rec := call(http.MethodPut, "/api/v1/presence", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	if rec := call(http.MethodPut, "/api/v1/presence", `{"grace_minutes":2,"emails":["family@example.com"],"chats":["777"]}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	// Per client: anna by email and Telegram, bob by Telegram, carol not at all.
	for name, body := range map[string]string{"anna": `{"email":true,"telegram":true}`, "bob": `{"telegram":true}`, "carol": `{"email":true}`} {
		if rec := call(http.MethodPut, "/api/v1/servers/openvpn/notify/"+name, body); rec.Code != http.StatusOK {
			t.Fatal(rec.Body)
		}
	}
	if rec := call(http.MethodPut, "/api/v1/servers/openvpn/notify/carol", `{}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	if rec := call(http.MethodGet, "/api/v1/servers/openvpn/notify", ""); rec.Body.String() != `{"anna":{"email":true,"telegram":true},"bob":{"email":false,"telegram":true}}`+"\n" {
		t.Fatalf("notify list: %s", rec.Body)
	}

	id := 0
	connect := func(name string, d time.Duration) {
		id++
		ts := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC).Add(d).Unix()
		if _, err := db.ExecContext(ctx, `INSERT INTO sessions (agent, name, client_id, real_address, connected_at, last_seen)
			VALUES ('openvpn', ?, ?, '203.0.113.7:5000', ?, ?)`, name, id, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	disconnect := func(name string, d time.Duration) {
		ts := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC).Add(d).Unix()
		if _, err := db.ExecContext(ctx, `UPDATE sessions SET disconnected_at = ?, last_seen = ? WHERE agent = 'openvpn' AND name = ? AND disconnected_at IS NULL`, ts, ts, name); err != nil {
			t.Fatal(err)
		}
	}
	check := func(d time.Duration) (mail, tgm []string) {
		at(d)
		srv.checkPresence(ctx)
		srv.mailWG.Wait()
		for _, m := range mails.take() {
			mail = append(mail, strings.Join(m.To, ",")+" | "+m.Subject)
		}
		return mail, tg.take()
	}

	// 12:00 anna, bob and carol are connected: the first check only learns.
	connect("anna", -3*time.Hour)
	connect("bob", -10*time.Minute)
	connect("carol", -time.Hour)
	if m, g := check(0); len(m)+len(g) != 0 {
		t.Fatalf("first check sent %v %v", m, g)
	}

	// anna leaves at 12:01: quiet within the grace time, reported after it.
	disconnect("anna", time.Minute)
	if m, g := check(time.Minute + 15*time.Second); len(m)+len(g) != 0 {
		t.Fatalf("within grace: %v %v", m, g)
	}
	m, g := check(3*time.Minute + 30*time.Second)
	if len(m) != 1 || m[0] != "family@example.com | trapdoor: anna is offline" {
		t.Fatalf("offline email: %v", m)
	}
	if len(g) != 1 || !strings.HasPrefix(g[0], "777: 🔴 anna is offline on openvpn since Nov 20 12:01") {
		t.Fatalf("offline telegram: %v", g)
	}
	// Reported once only.
	if m, g := check(5 * time.Minute); len(m)+len(g) != 0 {
		t.Fatalf("repeated: %v %v", m, g)
	}

	// bob reconnects quickly (a blip): nothing at all.
	disconnect("bob", 6*time.Minute)
	check(6*time.Minute + 10*time.Second)
	connect("bob", 6*time.Minute+40*time.Second)
	if m, g := check(7 * time.Minute); len(m)+len(g) != 0 {
		t.Fatalf("blip reported: %v %v", m, g)
	}

	// carol leaves: she has no notifications.
	disconnect("carol", 8*time.Minute)
	if m, g := check(11 * time.Minute); len(m)+len(g) != 0 {
		t.Fatalf("carol reported: %v %v", m, g)
	}

	// anna is back: reported on both channels, with where from.
	connect("anna", 20*time.Minute)
	m, g = check(20*time.Minute + 15*time.Second)
	if len(m) != 1 || !strings.HasSuffix(m[0], "anna is online") || len(g) != 1 || !strings.Contains(g[0], "🟢 anna is online on openvpn since Nov 20 12:20 from 203.0.113.7:5000") {
		t.Fatalf("online: %v %v", m, g)
	}
	// Telegram only for bob, and how long he was online.
	disconnect("bob", 30*time.Minute)
	m, g = check(33 * time.Minute)
	if len(m) != 0 || len(g) != 1 || !strings.Contains(g[0], "bob is offline") || !strings.Contains(g[0], "(was online 23 min)") {
		t.Fatalf("bob offline: %v %v", m, g)
	}

	// Without the channels' recipients set here, the admins and the bot's chats get them.
	if rec := call(http.MethodPut, "/api/v1/presence", `{"grace_minutes":2}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	disconnect("anna", 40*time.Minute)
	m, g = check(43 * time.Minute)
	if len(m) != 1 || !strings.HasPrefix(m[0], "admin@example.com |") || len(g) != 1 || !strings.HasPrefix(g[0], "4242: ") {
		t.Fatalf("default recipients: %v %v", m, g)
	}
}
