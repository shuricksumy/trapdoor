package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
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

	signIn := func(h http.Handler, id oidc.Identity) (*http.Cookie, string) {
		t.Helper()
		stub.id, stub.err = id, nil
		rec := get(h, "/api/v1/auth/oidc/callback?state=st-1&code=c1", sc)
		for _, c := range rec.Result().Cookies() {
			if c.Name == cookieName && c.Value != "" {
				return c, rec.Header().Get("Location")
			}
		}
		loc, _ := url.QueryUnescape(rec.Header().Get("Location"))
		return nil, loc
	}
	deps.OIDCAdminGroups = []string{"admin"}
	h = New(deps).Handler()

	// The first sign-in makes the person's own account (no local admin needed),
	// an admin by group; the next one finds it again by the subject.
	sess, loc := signIn(h, oidc.Identity{Subject: "sub-alex", Username: "alex", Groups: []string{"admin", "users"}})
	if sess == nil || sess.SameSite != http.SameSiteStrictMode || loc != "/" || stub.got != [3]string{"st-1", "st-1", "c1"} {
		t.Fatalf("callback: %v %s %v", sess, loc, stub.got)
	}
	if rec := get(h, "/api/v1/auth/me", sess); !strings.Contains(rec.Body.String(), `"username":"alex","method":"session","role":"admin","via":"oidc","source":"oidc"`) {
		t.Errorf("me: %d %s", rec.Code, rec.Body)
	}
	// a local account with the same name is not taken over: the second "alex" gets "alex-2"
	if _, err := a.CreateUser(ctx, "admin", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateUser(ctx, "bob", "correct horse battery staple 2"); err != nil {
		t.Fatal(err)
	}
	if c, _ := signIn(h, oidc.Identity{Subject: "sub-bob", Username: "bob", Groups: []string{"admin"}}); c == nil {
		t.Fatal("bob from Authelia not signed in")
	} else if rec := get(h, "/api/v1/auth/me", c); !strings.Contains(rec.Body.String(), `"username":"bob-2"`) {
		t.Errorf("same name as a local account: %s", rec.Body)
	}
	if c, _ := signIn(h, oidc.Identity{Subject: "sub-alex", Username: "alex", Groups: []string{"admin"}}); c == nil {
		t.Fatal("alex again")
	} else if us, _ := a.Users(ctx); len(us) != 4 {
		t.Errorf("accounts after the second sign-in: %d", len(us))
	}

	// Another allowed group (TD_OIDC_VIEWER_GROUPS) signs in read-only.
	viewer, _ := signIn(h, oidc.Identity{Subject: "sub-kid", Username: "kid", Groups: []string{"family"}})
	if viewer == nil {
		t.Fatal("viewer not signed in")
	}
	call := func(method, path string, c *http.Cookie) int {
		req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if rec := get(h, "/api/v1/auth/me", viewer); !strings.Contains(rec.Body.String(), `"role":"viewer"`) {
		t.Errorf("viewer me: %s", rec.Body)
	}
	for _, c := range []struct {
		method, path string
		ok           bool
	}{
		{http.MethodGet, "/api/v1/presence", true},                             // looks
		{http.MethodPut, "/api/v1/presence", false},                            // changes nothing
		{http.MethodPost, "/api/v1/servers/openvpn/certs", false},              // issues nothing
		{http.MethodDelete, "/api/v1/servers/openvpn/certs/anna", false},       // removes nothing
		{http.MethodPost, "/api/v1/servers/openvpn/tools/ping", false},         // runs nothing on the server
		{http.MethodGet, "/api/v1/servers/openvpn/certs/anna/ovpn", false},     // no keys
		{http.MethodGet, "/api/v1/servers/wg0/peers/anna/conf", false},         // no keys
		{http.MethodGet, "/api/v1/backups/files/trapdoor-all-1.tar.gz", false}, // no backups
		{http.MethodPost, "/api/v1/auth/logout", true},                         // can leave
	} {
		code := call(c.method, c.path, viewer)
		if (code == http.StatusForbidden) == c.ok {
			t.Errorf("viewer %s %s: %d", c.method, c.path, code)
		}
	}
	// an admin is not limited by it
	if code := call(http.MethodPut, "/api/v1/presence", sess); code == http.StatusForbidden {
		t.Errorf("admin refused: %d", code)
	}

	// A disabled account cannot sign in, and its sessions end.
	if err := a.SetDisabled(ctx, "alex", true); err != nil {
		t.Fatal(err)
	}
	if rec := get(h, "/api/v1/auth/me", sess); rec.Code != http.StatusUnauthorized {
		t.Errorf("disabled account still in: %d", rec.Code)
	}
	if c, loc := signIn(h, oidc.Identity{Subject: "sub-alex", Username: "alex", Groups: []string{"admin"}}); c != nil || !strings.Contains(loc, "disabled") {
		t.Errorf("disabled sign-in: %v %s", c, loc)
	}

	// TD_OIDC_USER: everyone signs in as that local account (as before accounts per person).
	deps.OIDCUser = "admin"
	if c, _ := signIn(New(deps).Handler(), oidc.Identity{Subject: "sub-x", Username: "x", Groups: []string{"admin"}}); c == nil {
		t.Error("TD_OIDC_USER not signed in")
	} else if rec := get(h, "/api/v1/auth/me", c); !strings.Contains(rec.Body.String(), `"username":"admin"`) {
		t.Errorf("TD_OIDC_USER: %s", rec.Body)
	}
	deps.OIDCUser = "nobody"
	if c, loc := signIn(New(deps).Handler(), oidc.Identity{Subject: "sub-x", Username: "x", Groups: []string{"admin"}}); c != nil || !strings.Contains(loc, "TD_OIDC_USER") {
		t.Errorf("unknown TD_OIDC_USER: %v %s", c, loc)
	}
	deps.OIDCUser = ""

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

// TD_PASSWORD_LOGIN_FROM: the password login only from allowed networks,
// the client address taken through trusted proxies.
func TestPasswordLoginFrom(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "td.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	a := auth.New(db, auth.Options{})
	if _, err := a.CreateUser(ctx, "admin", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	h := New(Deps{Stats: &history.Stats{DB: db}, Auth: a, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("172.20.0.0/24")},
		PasswordFrom:   []netip.Prefix{netip.MustParsePrefix("10.8.0.0/24"), netip.MustParsePrefix("192.168.1.5/32")}}).Handler()
	try := func(remote, xff string) (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"correct horse battery staple"}`))
		req.RemoteAddr = remote + ":5555"
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		m := httptest.NewRequest(http.MethodGet, "/api/v1/auth/methods", nil)
		m.RemoteAddr = req.RemoteAddr
		m.Header = req.Header.Clone()
		mrec := httptest.NewRecorder()
		h.ServeHTTP(mrec, m)
		return rec.Code, mrec.Body.String()
	}
	for _, c := range []struct {
		remote, xff string
		ok          bool
	}{
		{"10.8.0.6", "", true},                          // a VPN client, directly
		{"192.168.1.5", "", true},                       // a single address
		{"203.0.113.9", "", false},                      // the internet
		{"172.20.0.2", "10.8.0.6", true},                // through the trusted proxy, from the VPN
		{"172.20.0.2", "203.0.113.9", false},            // through the trusted proxy, from the internet
		{"203.0.113.9", "10.8.0.6", false},              // an untrusted peer cannot claim a VPN address
		{"172.20.0.2", "10.8.0.6, 198.51.100.7", false}, // the last untrusted hop counts
	} {
		code, methods := try(c.remote, c.xff)
		if (code == http.StatusOK) != c.ok || (code == http.StatusForbidden) == c.ok || strings.Contains(methods, `"password":true`) != c.ok {
			t.Errorf("%s via %q: login %d, methods %s", c.remote, c.xff, code, methods)
		}
	}
}
