package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shuricksumy/trapdoor/internal/auth"
	"github.com/shuricksumy/trapdoor/internal/history"
	"github.com/shuricksumy/trapdoor/internal/oidc"
	"github.com/shuricksumy/trapdoor/internal/store"
)

// stubOIDC: Start hands out "st-1"; Finish answers with id / err.
type stubOIDC struct {
	id  oidc.Identity
	err error
	got [3]string
}

func (s *stubOIDC) Start(context.Context) (string, string, error) {
	return "st-1", "https://auth.example.com/authorize?state=st-1", nil
}

func (s *stubOIDC) Finish(_ context.Context, cookieState, state, code string) (oidc.Identity, error) {
	s.got = [3]string{cookieState, state, code}
	return s.id, s.err
}

func TestOIDCLogin(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "td.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	a := auth.New(db, auth.Options{})
	stub := &stubOIDC{}
	deps := Deps{Stats: &history.Stats{DB: db}, Auth: a, CookieSecure: true, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	get := func(h http.Handler, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// Off: the login page shows only the password form.
	off := New(deps).Handler()
	if rec := get(off, "/api/v1/auth/methods"); !strings.Contains(rec.Body.String(), `"password":true`) || strings.Contains(rec.Body.String(), "oidc") {
		t.Errorf("methods without OIDC: %s", rec.Body)
	}
	if rec := get(off, "/api/v1/auth/oidc"); rec.Code != http.StatusNotFound {
		t.Errorf("start without OIDC: %d", rec.Code)
	}

	deps.OIDC, deps.OIDCName = stub, "Authelia"
	h := New(deps).Handler()
	if rec := get(h, "/api/v1/auth/methods"); !strings.Contains(rec.Body.String(), `"oidc":"Authelia"`) {
		t.Errorf("methods: %s", rec.Body)
	}
	// Start: to the provider, the state in a Lax cookie for the callback only.
	rec := get(h, "/api/v1/auth/oidc")
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "https://auth.example.com/authorize") {
		t.Fatalf("start: %d %v", rec.Code, rec.Header())
	}
	sc := rec.Result().Cookies()[0]
	if sc.Name != oidcCookie || sc.Value != "st-1" || sc.SameSite != http.SameSiteLaxMode || !sc.HttpOnly || !sc.Secure || sc.Path != "/api/v1/auth/oidc" {
		t.Errorf("state cookie: %+v", sc)
	}

	// No local admin yet: refused with a hint.
	stub.id = oidc.Identity{Username: "alex", Groups: []string{"admin"}}
	rec = get(h, "/api/v1/auth/oidc/callback?state=st-1&code=c1", sc)
	if loc, _ := url.QueryUnescape(rec.Header().Get("Location")); rec.Code != http.StatusSeeOther || !strings.Contains(loc, "no local admin") {
		t.Errorf("no admin: %d %s", rec.Code, loc)
	}
	if _, err := a.CreateUser(ctx, "admin", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateUser(ctx, "backup", "correct horse battery staple 2"); err != nil {
		t.Fatal(err)
	}

	// Signed in: a session as the first admin, back to the app.
	rec = get(h, "/api/v1/auth/oidc/callback?state=st-1&code=c1", sc)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" || stub.got != [3]string{"st-1", "st-1", "c1"} {
		t.Fatalf("callback: %d %v %v", rec.Code, rec.Header(), stub.got)
	}
	var sess *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			sess = c
		}
	}
	if sess == nil || sess.SameSite != http.SameSiteStrictMode {
		t.Fatalf("no session cookie: %v", rec.Result().Cookies())
	}
	if rec := get(h, "/api/v1/auth/me", sess); !strings.Contains(rec.Body.String(), `"username":"admin"`) {
		t.Errorf("me: %d %s", rec.Code, rec.Body)
	}

	// TD_OIDC_USER picks the admin; a missing one is an error.
	deps.OIDCUser = "backup"
	rec = get(New(deps).Handler(), "/api/v1/auth/oidc/callback?state=st-1&code=c1", sc)
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			if r := get(h, "/api/v1/auth/me", c); !strings.Contains(r.Body.String(), `"username":"backup"`) {
				t.Errorf("TD_OIDC_USER: %s", r.Body)
			}
		}
	}
	deps.OIDCUser = "nobody"
	if rec := get(New(deps).Handler(), "/api/v1/auth/oidc/callback?state=st-1&code=c1", sc); !strings.Contains(rec.Header().Get("Location"), "error=") {
		t.Errorf("unknown TD_OIDC_USER signed in: %v", rec.Header())
	}

	// Refusals go back to the login page with a reason, no session.
	for name, c := range map[string]struct {
		err  error
		path string
		want string
	}{
		"group":    {oidc.ErrNotAllowed, "/api/v1/auth/oidc/callback?state=st-1&code=c1", "not in a group allowed"},
		"state":    {oidc.ErrState, "/api/v1/auth/oidc/callback?state=st-1&code=c1", "another browser"},
		"provider": {nil, "/api/v1/auth/oidc/callback?error=access_denied", "said no: access_denied"},
	} {
		stub.err = c.err
		rec := get(h, c.path, sc)
		loc, _ := url.QueryUnescape(rec.Header().Get("Location"))
		if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/login?error=") || !strings.Contains(loc, c.want) {
			t.Errorf("%s: %d %s", name, rec.Code, loc)
		}
		for _, ck := range rec.Result().Cookies() {
			if ck.Name == cookieName && ck.Value != "" {
				t.Errorf("%s: session cookie set", name)
			}
		}
	}
}
