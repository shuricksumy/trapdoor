package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type reqOpt func(*http.Request)

func header(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }
func cookie(c *http.Cookie) reqOpt {
	return func(r *http.Request) {
		if c != nil {
			r.AddCookie(c)
		}
	}
}
func remote(addr string) reqOpt { return func(r *http.Request) { r.RemoteAddr = addr } }

func (e *testEnv) do(method, path, body string, opts ...reqOpt) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "vpn.example.com"
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			return c
		}
	}
	return nil
}

func (e *testEnv) login(user, pw string, opts ...reqOpt) *httptest.ResponseRecorder {
	return e.do(http.MethodPost, "/api/v1/auth/login", `{"username":"`+user+`","password":"`+pw+`"}`, opts...)
}

func TestLoginFlow(t *testing.T) {
	e := newTestEnv(t, "")
	if _, err := e.auth.CreateUser(context.Background(), "admin", "long-enough-pw"); err != nil {
		t.Fatal(err)
	}

	if rec := e.do(http.MethodGet, "/api/v1/auth/me", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("me without login = %d", rec.Code)
	}
	rec := e.login("admin", "wrong-password")
	if rec.Code != http.StatusUnauthorized || sessionCookie(t, rec) != nil {
		t.Fatalf("bad login = %d", rec.Code)
	}

	rec = e.login("admin", "long-enough-pw")
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d %s", rec.Code, rec.Body.String())
	}
	c := sessionCookie(t, rec)
	if c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Fatalf("cookie = %+v", c)
	}
	if rec.Header().Get("X-Frame-Options") != "DENY" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("security headers missing: %v", rec.Header())
	}

	rec = e.do(http.MethodGet, "/api/v1/auth/me", "", cookie(c))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"username":"admin"`) {
		t.Fatalf("me = %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(http.MethodGet, "/api/v1/stats", "", cookie(c)); rec.Code != http.StatusOK {
		t.Errorf("stats with session = %d", rec.Code)
	}

	// CSRF: state-changing request from another site is refused.
	if rec := e.do(http.MethodPost, "/api/v1/auth/logout", "", cookie(c), header("Sec-Fetch-Site", "cross-site")); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site logout = %d, want 403", rec.Code)
	}
	if rec := e.do(http.MethodPost, "/api/v1/auth/logout", "", cookie(c), header("Origin", "https://evil.example")); rec.Code != http.StatusForbidden {
		t.Errorf("foreign Origin = %d, want 403", rec.Code)
	}
	if rec := e.login("admin", "long-enough-pw", header("Sec-Fetch-Site", "cross-site")); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site login = %d, want 403", rec.Code)
	}

	// Same-origin logout ends the session.
	rec = e.do(http.MethodPost, "/api/v1/auth/logout", "", cookie(c), header("Origin", "https://vpn.example.com"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d", rec.Code)
	}
	if rec := e.do(http.MethodGet, "/api/v1/auth/me", "", cookie(c)); rec.Code != http.StatusUnauthorized {
		t.Errorf("me after logout = %d", rec.Code)
	}
}

func TestChangePassword(t *testing.T) {
	e := newTestEnv(t, token)
	_, _ = e.auth.CreateUser(context.Background(), "admin", "long-enough-pw")
	c := sessionCookie(t, e.login("admin", "long-enough-pw"))

	cases := []struct {
		body string
		want int
	}{
		{`{"current_password":"wrong-one-here","new_password":"brand-new-password"}`, http.StatusForbidden},
		{`{"current_password":"long-enough-pw","new_password":"short"}`, http.StatusBadRequest},
		{`{"current_password":"long-enough-pw","new_password":"brand-new-password"}`, http.StatusNoContent},
	}
	for _, tc := range cases {
		if rec := e.do(http.MethodPost, "/api/v1/auth/password", tc.body, cookie(c)); rec.Code != tc.want {
			t.Errorf("%s: %d, want %d", tc.body, rec.Code, tc.want)
		}
	}
	if rec := e.do(http.MethodGet, "/api/v1/auth/me", "", cookie(c)); rec.Code != http.StatusUnauthorized {
		t.Error("old session must end after password change")
	}
	if rec := e.login("admin", "brand-new-password"); rec.Code != http.StatusOK {
		t.Errorf("login with new password = %d", rec.Code)
	}
	// The API token is not a user and cannot change passwords.
	if rec := e.do(http.MethodPost, "/api/v1/auth/password", `{}`, header("Authorization", "Bearer "+token)); rec.Code != http.StatusForbidden {
		t.Errorf("token password change = %d", rec.Code)
	}
}

func TestLoginThrottling(t *testing.T) {
	e := newTestEnv(t, "")
	_, _ = e.auth.CreateUser(context.Background(), "admin", "long-enough-pw")
	for range 3 {
		e.login("admin", "wrong-password", remote("192.0.2.1:1000"))
	}
	rec := e.login("admin", "long-enough-pw", remote("192.0.2.1:1000"))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("throttled login = %d (Retry-After %q)", rec.Code, rec.Header().Get("Retry-After"))
	}
	if rec := e.login("admin", "long-enough-pw", remote("192.0.2.2:1000")); rec.Code != http.StatusOK {
		t.Errorf("other IP = %d", rec.Code)
	}
	// Behind a trusted proxy, the X-Forwarded-For client is throttled, not the proxy.
	for range 3 {
		e.login("admin", "wrong-password", remote("10.1.1.1:443"), header("X-Forwarded-For", "203.0.113.9"))
	}
	if rec := e.login("admin", "long-enough-pw", remote("10.1.1.1:443"), header("X-Forwarded-For", "203.0.113.9")); rec.Code != http.StatusTooManyRequests {
		t.Errorf("forwarded client not throttled: %d", rec.Code)
	}
	if rec := e.login("admin", "long-enough-pw", remote("10.1.1.1:443"), header("X-Forwarded-For", "203.0.113.10")); rec.Code != http.StatusOK {
		t.Errorf("other forwarded client = %d", rec.Code)
	}
	// An untrusted peer cannot spoof X-Forwarded-For to dodge the limit.
	if rec := e.login("admin", "long-enough-pw", remote("192.0.2.1:1000"), header("X-Forwarded-For", "198.51.100.1")); rec.Code != http.StatusTooManyRequests {
		t.Errorf("spoofed XFF bypassed throttling: %d", rec.Code)
	}
}
