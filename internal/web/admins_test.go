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

	"github.com/shuricksumy/trapdoor/internal/auth"
	"github.com/shuricksumy/trapdoor/internal/history"
	"github.com/shuricksumy/trapdoor/internal/store"
)

func TestAdmins(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "td.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	a := auth.New(db, auth.Options{})
	h := New(Deps{Stats: &history.Stats{DB: db}, Auth: a, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()
	session := func(name string) string {
		t.Helper()
		var u auth.User
		us, _ := a.Users(ctx)
		for _, x := range us {
			if x.Username == name {
				u = x
			}
		}
		tok, _, err := a.StartSession(ctx, u, "203.0.113.7", "test")
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	call := func(tok, method, path, body string) (int, string) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: cookieName, Value: tok})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	if _, err := a.CreateUser(ctx, "admin", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateUserRole(ctx, "kid", "correct horse battery", auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	adm, kid := session("admin"), session("kid")

	// a viewer sees nothing here
	if code, _ := call(kid, http.MethodGet, "/api/v1/admins", ""); code != http.StatusForbidden {
		t.Errorf("viewer list: %d", code)
	}
	code, body := call(adm, http.MethodGet, "/api/v1/admins", "")
	var list []Account
	_ = json.Unmarshal([]byte(body), &list)
	if code != 200 || len(list) != 2 || !list[0].You || list[0].Sessions != 1 || list[0].LastLoginIP != "203.0.113.7" || list[1].Role != "viewer" {
		t.Fatalf("list: %d %s", code, body)
	}

	// add, bad input refused
	for _, bad := range []string{`{"username":"a b","password":"correct horse battery"}`, `{"username":"x","password":"short"}`, `{"username":"x","password":"correct horse battery","role":"owner"}`} {
		if code, _ := call(adm, http.MethodPost, "/api/v1/admins", bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, code)
		}
	}
	if code, body := call(adm, http.MethodPost, "/api/v1/admins", `{"username":"ops","password":"correct horse battery","role":"admin"}`); code != http.StatusCreated || !strings.Contains(body, `"role":"admin"`) {
		t.Fatalf("add: %d %s", code, body)
	}
	if code, _ := call(adm, http.MethodPost, "/api/v1/admins", `{"username":"ops","password":"correct horse battery"}`); code != http.StatusConflict {
		t.Errorf("add twice: %d", code)
	}

	// role and turning off; not yourself
	if code, body := call(adm, http.MethodPut, "/api/v1/admins/kid", `{"role":"admin"}`); code != 200 || !strings.Contains(body, `"username":"kid"`) {
		t.Errorf("promote: %d %s", code, body)
	}
	if code, _ := call(adm, http.MethodPut, "/api/v1/admins/admin", `{"disabled":true}`); code != http.StatusConflict {
		t.Errorf("turn yourself off: %d", code)
	}
	if code, _ := call(adm, http.MethodDelete, "/api/v1/admins/admin", ""); code != http.StatusConflict {
		t.Errorf("delete yourself: %d", code)
	}
	if code, _ := call(adm, http.MethodPut, "/api/v1/admins/kid", `{"disabled":true}`); code != 200 {
		t.Errorf("turn kid off: %d", code)
	}
	if code, _ := call(kid, http.MethodGet, "/api/v1/auth/me", ""); code != http.StatusUnauthorized {
		t.Errorf("kid still in after being turned off: %d", code)
	}

	// a new password for ops; logged out everywhere
	opsTok := session("ops")
	if code, _ := call(adm, http.MethodPost, "/api/v1/admins/ops/password", `{"password":"another long password"}`); code != http.StatusNoContent {
		t.Errorf("password: %d", code)
	}
	if code, _ := call(opsTok, http.MethodGet, "/api/v1/auth/me", ""); code != http.StatusUnauthorized {
		t.Errorf("ops still in after a new password: %d", code)
	}
	if _, err := a.Authenticate(ctx, "ops", "another long password"); err != nil {
		t.Errorf("new password: %v", err)
	}
	_ = session("ops") // a session to end
	if code, body := call(adm, http.MethodPost, "/api/v1/admins/ops/logout", ""); code != 200 || !strings.Contains(body, `"ended":1`) {
		t.Errorf("log out ops: %d %s", code, body)
	}

	// delete; the last enabled admin stays
	if code, _ := call(adm, http.MethodDelete, "/api/v1/admins/ops", ""); code != http.StatusNoContent {
		t.Errorf("delete ops: %d", code)
	}
	if code, body := call(adm, http.MethodPut, "/api/v1/admins/admin", `{"role":"viewer"}`); code != http.StatusConflict || !strings.Contains(body, "at least one enabled admin") {
		t.Errorf("demote the last enabled admin: %d %s", code, body)
	}
	if code, _ := call(adm, http.MethodDelete, "/api/v1/admins/nobody", ""); code != http.StatusNotFound {
		t.Errorf("unknown: %d", code)
	}
}
