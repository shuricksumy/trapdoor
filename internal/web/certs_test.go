package web

import (
	"context"

	"github.com/shuricksumy/tunnelward/internal/api"
	"net/http"
	"strings"
	"testing"
)

func TestCertManagement(t *testing.T) {
	e := newTestEnv(t, "")
	_, _ = e.auth.CreateUser(context.Background(), "admin", "long-enough-pw")
	c := sessionCookie(t, e.login("admin", "long-enough-pw"))
	same := header("Origin", "https://vpn.example.com")

	if rec := e.do(http.MethodPost, "/api/v1/certs", `{"name":"alice"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("issue without login = %d", rec.Code)
	}
	if rec := e.do(http.MethodPost, "/api/v1/certs", `{"name":"alice"}`, cookie(c), header("Sec-Fetch-Site", "cross-site")); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site issue = %d", rec.Code)
	}

	steps := []struct {
		method, path, body string
		want               int
		contains           string
	}{
		{http.MethodPost, "/api/v1/certs", `{"name":"alice","validity_days":30}`, http.StatusCreated, `"status":"valid"`},
		{http.MethodPost, "/api/v1/certs", `{"name":"alice"}`, http.StatusConflict, "exists"},
		{http.MethodPost, "/api/v1/certs", `{"name":"../x"}`, http.StatusBadRequest, "invalid"},
		{http.MethodPost, "/api/v1/certs", `{"name":"x","bogus":1}`, http.StatusBadRequest, ""},
		{http.MethodGet, "/api/v1/certs", "", http.StatusOK, `"name":"alice"`},
		{http.MethodGet, "/api/v1/certs/alice", "", http.StatusOK, `"current":true`},
		{http.MethodGet, "/api/v1/certs/nobody", "", http.StatusNotFound, ""},
		{http.MethodGet, "/api/v1/certs/alice/ovpn", "", http.StatusServiceUnavailable, "template"}, // no client-template.txt
		{http.MethodPost, "/api/v1/certs/alice/revoke", "", http.StatusOK, `"status":"revoked"`},
		{http.MethodPost, "/api/v1/certs/alice/revoke", "", http.StatusConflict, ""},
		{http.MethodPost, "/api/v1/certs/alice/unrevoke", "", http.StatusOK, `"status":"valid"`},
		{http.MethodPost, "/api/v1/certs/alice/renew", `{"validity_days":10}`, http.StatusOK, `"status":"valid"`},
		{http.MethodDelete, "/api/v1/certs/alice/sessions", "", http.StatusBadGateway, ""}, // management unreachable
		{http.MethodDelete, "/api/v1/sessions/abc", "", http.StatusBadRequest, ""},
		{http.MethodGet, "/api/v1/certs/alice/auth", "", http.StatusOK, `"totp":false`},
		{http.MethodPut, "/api/v1/certs/alice/auth", `{"totp":"enable","static_password":"pin1"}`, http.StatusOK, `"otpauth_uri":"otpauth://totp/`},
		{http.MethodGet, "/api/v1/certs/alice/auth", "", http.StatusOK, `"totp":true`},
		{http.MethodPut, "/api/v1/certs/alice/auth", `{"totp":"bogus"}`, http.StatusBadRequest, ""},
		{http.MethodGet, "/api/v1/network", "", http.StatusOK, `"topology":"net30"`},
		{http.MethodPut, "/api/v1/certs/alice/network", `{"routes":["192.168.1.0/24"],"lan":[],"full_tunnel":true}`, http.StatusOK, `"full_tunnel":true`},
		{http.MethodGet, "/api/v1/certs/alice/network", "", http.StatusOK, `"192.168.1.0/24"`},
		{http.MethodPut, "/api/v1/certs/alice/network", `{"routes":["bad"],"lan":[]}`, http.StatusBadRequest, ""},
		{http.MethodDelete, "/api/v1/certs/alice", "", http.StatusNoContent, ""},
		{http.MethodGet, "/api/v1/certs/alice", "", http.StatusOK, `"status":"revoked"`},
	}
	for _, st := range steps {
		rec := e.do(st.method, st.path, st.body, cookie(c), same)
		if rec.Code != st.want || !strings.Contains(rec.Body.String(), st.contains) {
			t.Errorf("%s %s %s: %d %s; want %d containing %q", st.method, st.path, st.body, rec.Code, rec.Body.String(), st.want, st.contains)
		}
	}
}

func TestRenewMany(t *testing.T) {
	e := newTestEnv(t, token)
	bearer := header("Authorization", "Bearer "+token)
	for _, n := range []string{"a1", "a2"} {
		if rec := e.do(http.MethodPost, "/api/v1/certs", `{"name":"`+n+`"}`, bearer); rec.Code != http.StatusCreated {
			t.Fatalf("issue %s: %d", n, rec.Code)
		}
	}
	rec := e.do(http.MethodPost, "/api/v1/certs/renew", `{"names":["a1","a2","ghost","../x","a1"],"validity_days":90}`, bearer)
	if rec.Code != http.StatusOK {
		t.Fatalf("renew many = %d %s", rec.Code, rec.Body.String())
	}
	res := decode[[]api.RenewResult](t, rec)
	if len(res) != 4 || !res[0].OK || !res[1].OK || res[2].OK || res[3].OK || res[0].Cert == nil {
		t.Fatalf("results = %+v", res)
	}
	if !strings.Contains(res[2].Error, "not found") || res[3].Error != "invalid certificate name" {
		t.Errorf("errors = %q / %q", res[2].Error, res[3].Error)
	}
	if rec := e.do(http.MethodPost, "/api/v1/certs/renew", `{"names":[]}`, bearer); rec.Code != http.StatusBadRequest {
		t.Errorf("empty list = %d", rec.Code)
	}
}
