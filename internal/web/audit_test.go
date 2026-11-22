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

	"github.com/shuricksumy/trapdoor/internal/auth"
	"github.com/shuricksumy/trapdoor/internal/history"
	"github.com/shuricksumy/trapdoor/internal/store"
)

func TestAuditLog(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "td.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	a := auth.New(db, auth.Options{})
	clock := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	h := New(Deps{Stats: &history.Stats{DB: db}, Auth: a, Location: time.UTC, Now: func() time.Time { return clock },
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()
	if _, err := a.CreateUser(ctx, "admin", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateUserRole(ctx, "kid", "correct horse battery", auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	req := func(method, path, body, tok string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.RemoteAddr = "203.0.113.5:4444"
		if tok != "" {
			r.AddCookie(&http.Cookie{Name: cookieName, Value: tok})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	login := func(user, pass string) string {
		rec := req(http.MethodPost, "/api/v1/auth/login", `{"username":"`+user+`","password":"`+pass+`"}`, "")
		for _, c := range rec.Result().Cookies() {
			if c.Name == cookieName {
				return c.Value
			}
		}
		return ""
	}
	login("admin", "wrong password")
	adm := login("admin", "correct horse battery")
	kid := login("kid", "correct horse battery")
	clock = clock.Add(time.Hour)
	// a change, with a value a spreadsheet would run as a formula
	if rec := req(http.MethodPost, "/api/v1/admins", `{"username":"ops","password":"correct horse battery","role":"viewer"}`, adm); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body)
	}
	if rec := req(http.MethodPut, "/api/v1/presence", `{"grace_minutes":2,"email":"","chat":""}`, adm); rec.Code != 200 {
		t.Fatal(rec.Body)
	}

	if rec := req(http.MethodGet, "/api/v1/audit", "", kid); rec.Code != http.StatusForbidden {
		t.Errorf("viewer reads the audit log: %d", rec.Code)
	}
	rec := req(http.MethodGet, "/api/v1/audit", "", adm)
	var page AuditPage
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if rec.Code != 200 || page.Total != 5 || page.Entries[0].Action != "presence.settings" || len(page.Users) != 2 {
		t.Fatalf("audit: %d %s", rec.Code, rec.Body)
	}
	last := page.Entries[len(page.Entries)-1]
	if last.Action != "login.failed" || last.Username != "admin" || last.Via != "password" || last.IP != "203.0.113.5" {
		t.Errorf("failed login: %+v", last)
	}
	if e := page.Entries[1]; e.Action != "admin.add" || e.Target != "ops (viewer)" || e.Via != "password" {
		t.Errorf("change: %+v", e)
	}
	for q, want := range map[string]int{
		"action=login":                  3, // login and login.failed
		"action=admin":                  1,
		"user=kid":                      1,
		"q=ops":                         1,
		"from=2026-11-22&to=2026-11-22": 5,
		"from=2026-11-23":               0,
		"limit=2&offset=4":              5, // the total stays
	} {
		var p AuditPage
		_ = json.Unmarshal(req(http.MethodGet, "/api/v1/audit?"+q, "", adm).Body.Bytes(), &p)
		if p.Total != want {
			t.Errorf("%s: %d", q, p.Total)
		}
	}
	if rec := req(http.MethodGet, "/api/v1/audit?from=yesterday", "", adm); rec.Code != http.StatusBadRequest {
		t.Errorf("bad date: %d", rec.Code)
	}
	csv := req(http.MethodGet, "/api/v1/audit.csv", "", adm).Body.String()
	lines := strings.Split(strings.TrimSpace(csv), "\n")
	if len(lines) != 6 || !strings.HasPrefix(lines[0], "time,user,via,address,action") || !strings.Contains(lines[1], "login.failed") {
		t.Errorf("csv:\n%s", csv)
	}
	if got := csvSafe("=cmd|' /C calc'!A0"); !strings.HasPrefix(got, "'=") {
		t.Errorf("formula kept: %s", got)
	}
}
