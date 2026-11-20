package web

import (
	"context"
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

	"github.com/shuricksumy/trapdoor/internal/auth"
	"github.com/shuricksumy/trapdoor/internal/history"
	"github.com/shuricksumy/trapdoor/internal/store"
)

// tgbox records the Telegram messages a test server sends.
type tgbox struct {
	mu   sync.Mutex
	sent []string // "chat: text"
	fail map[string]bool
}

func (b *tgbox) send(_ context.Context, token, chat, text string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if token == "" {
		return errors.New("no token")
	}
	if b.fail[chat] {
		return errors.New("telegram: Bad Request: chat not found")
	}
	b.sent = append(b.sent, chat+": "+text)
	return nil
}

func (b *tgbox) take() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.sent
	b.sent = nil
	return out
}

const tgToken = "123456789:AAEhBP0av28BmFbW3Eq7Yqk0WbqXyZabc12"

func TestTelegram(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "td.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	box := &tgbox{fail: map[string]bool{"-1": true}}
	srv := New(Deps{Agent: nil, Stats: &history.Stats{DB: db}, Auth: auth.New(db, auth.Options{}), APIToken: "tok",
		Location: time.UTC, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), SendTelegram: box.send,
		PublicURL: "https://vpn-admin.example.com"})
	h := srv.Handler()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// Not set up: the test refuses, notifications are quiet.
	if rec := call(http.MethodPost, "/api/v1/telegram/test", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("test without setup: %d", rec.Code)
	}
	srv.notify(ctx, func(Notify) bool { return true }, "x", "y")
	srv.mailWG.Wait()
	if n := len(box.take()); n != 0 {
		t.Errorf("sent %d without setup", n)
	}

	for _, bad := range []string{
		`{"enabled":true,"token":"123:short","chats":["4242"]}`,
		`{"enabled":true,"token":"` + tgToken + `","chats":["not a chat"]}`,
		`{"enabled":true,"chats":["4242"]}`, // no token stored yet
	} {
		if rec := call(http.MethodPut, "/api/v1/telegram", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	good := `{"enabled":true,"token":"` + tgToken + `","chats":["4242, @ops_channel"],"notify":{"backup_failed":true,"node_down":false,"cert_expiry":true}}`
	if rec := call(http.MethodPut, "/api/v1/telegram", good); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "AAEh") {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	// The token is kept when the form sends none, and never shown.
	if rec := call(http.MethodPut, "/api/v1/telegram", strings.Replace(good, `"token":"`+tgToken+`",`, "", 1)); rec.Code != http.StatusOK {
		t.Fatalf("save again: %d %s", rec.Code, rec.Body)
	}
	rec := call(http.MethodGet, "/api/v1/telegram", "")
	if strings.Contains(rec.Body.String(), "AAEh") || !strings.Contains(rec.Body.String(), `"token_set":true`) || !strings.Contains(rec.Body.String(), `"chats":["4242","@ops_channel"]`) {
		t.Fatalf("get: %s", rec.Body)
	}

	if rec := call(http.MethodPost, "/api/v1/telegram/test", ""); rec.Code != http.StatusOK {
		t.Fatalf("test: %d %s", rec.Code, rec.Body)
	}
	if sent := box.take(); len(sent) != 2 || !strings.HasPrefix(sent[0], "4242: trapdoor: test") || !strings.Contains(sent[1], "vpn-admin.example.com") {
		t.Fatalf("test messages: %v", sent)
	}
	// A chat that fails: an error, and the status says so.
	if rec := call(http.MethodPost, "/api/v1/telegram/test", `{"chat":"-1"}`); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "chat not found") {
		t.Fatalf("failing chat: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodGet, "/api/v1/telegram", ""); !strings.Contains(rec.Body.String(), `"ok":false`) {
		t.Errorf("status after a failure: %s", rec.Body)
	}

	// Notifications follow the kinds chosen for Telegram.
	srv.notify(ctx, func(n Notify) bool { return n.BackupFailed }, "backup failed", "disk full")
	srv.notify(ctx, func(n Notify) bool { return n.NodeDown }, "node office is not reachable", "since 12:00")
	srv.mailWG.Wait()
	sent := box.take()
	if len(sent) != 2 || !strings.Contains(sent[0], "trapdoor: backup failed\n\ndisk full") {
		t.Fatalf("notifications: %q", sent)
	}

	// Off keeps the settings but sends nothing; clearing the token turns it off for good.
	if rec := call(http.MethodPut, "/api/v1/telegram", `{"enabled":false,"chats":["4242"]}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	srv.notify(ctx, func(Notify) bool { return true }, "x", "y")
	srv.mailWG.Wait()
	if n := len(box.take()); n != 0 {
		t.Errorf("sent %d while off", n)
	}
	if rec := call(http.MethodPut, "/api/v1/telegram", `{"enabled":false,"clear_token":true,"chats":[]}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"token_set":false`) {
		t.Fatalf("clear: %s", rec.Body)
	}
}
