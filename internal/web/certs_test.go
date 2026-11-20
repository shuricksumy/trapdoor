package web

import (
	"context"

	"github.com/shuricksumy/trapdoor/internal/api"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCertManagement(t *testing.T) {
	e := newTestEnv(t, "")
	_, _ = e.auth.CreateUser(context.Background(), "admin", "long-enough-pw")
	c := sessionCookie(t, e.login("admin", "long-enough-pw"))
	same := header("Origin", "https://vpn.example.com")

	if rec := e.do(http.MethodPost, "/api/v1/servers/openvpn/certs", `{"name":"alice"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("issue without login = %d", rec.Code)
	}
	if rec := e.do(http.MethodPost, "/api/v1/servers/openvpn/certs", `{"name":"alice"}`, cookie(c), header("Sec-Fetch-Site", "cross-site")); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site issue = %d", rec.Code)
	}

	steps := []struct {
		method, path, body string
		want               int
		contains           string
	}{
		{http.MethodPost, "/api/v1/servers/openvpn/certs", `{"name":"alice","validity_days":30}`, http.StatusCreated, `"status":"valid"`},
		{http.MethodPost, "/api/v1/servers/openvpn/certs", `{"name":"alice"}`, http.StatusConflict, "exists"},
		{http.MethodPost, "/api/v1/servers/openvpn/certs", `{"name":"../x"}`, http.StatusBadRequest, "invalid"},
		{http.MethodPost, "/api/v1/servers/openvpn/certs", `{"name":"x","bogus":1}`, http.StatusBadRequest, ""},
		{http.MethodGet, "/api/v1/servers/openvpn/certs", "", http.StatusOK, `"name":"alice"`},
		{http.MethodGet, "/api/v1/servers/openvpn/certs/alice", "", http.StatusOK, `"current":true`},
		{http.MethodGet, "/api/v1/servers/openvpn/certs/nobody", "", http.StatusNotFound, ""},
		{http.MethodGet, "/api/v1/servers/openvpn/certs/alice/ovpn", "", http.StatusServiceUnavailable, "template"}, // no client-template.txt
		{http.MethodPost, "/api/v1/servers/openvpn/certs/alice/revoke", "", http.StatusOK, `"status":"revoked"`},
		{http.MethodPost, "/api/v1/servers/openvpn/certs/alice/revoke", "", http.StatusConflict, ""},
		{http.MethodPost, "/api/v1/servers/openvpn/certs/alice/unrevoke", "", http.StatusOK, `"status":"valid"`},
		{http.MethodPost, "/api/v1/servers/openvpn/certs/alice/renew", `{"validity_days":10}`, http.StatusOK, `"status":"valid"`},
		{http.MethodDelete, "/api/v1/servers/openvpn/certs/alice/sessions", "", http.StatusBadGateway, ""}, // management unreachable
		{http.MethodDelete, "/api/v1/servers/openvpn/sessions/abc", "", http.StatusBadRequest, ""},
		{http.MethodGet, "/api/v1/servers/openvpn/certs/alice/auth", "", http.StatusOK, `"totp":false`},
		{http.MethodPut, "/api/v1/servers/openvpn/certs/alice/auth", `{"totp":"enable","static_password":"pin1"}`, http.StatusOK, `"otpauth_uri":"otpauth://totp/`},
		{http.MethodGet, "/api/v1/servers/openvpn/certs/alice/auth", "", http.StatusOK, `"totp":true`},
		{http.MethodPut, "/api/v1/servers/openvpn/certs/alice/auth", `{"totp":"bogus"}`, http.StatusBadRequest, ""},
		{http.MethodPut, "/api/v1/servers/openvpn/certs/alice/notes", `{"notes":"Alice's phone"}`, http.StatusOK, `"notes":"Alice's phone"`},
		{http.MethodGet, "/api/v1/servers/openvpn/certs/alice", "", http.StatusOK, `"notes":"Alice's phone"`},
		{http.MethodGet, "/api/v1/servers/openvpn/certs/alice/auth", "", http.StatusOK, `"totp":true`}, // 2FA lines kept
		{http.MethodPut, "/api/v1/servers/openvpn/certs/alice/notes", `{"notes":"a\nb"}`, http.StatusBadRequest, ""},
		{http.MethodGet, "/api/v1/servers/openvpn/network", "", http.StatusOK, `"topology":"net30"`},
		{http.MethodPut, "/api/v1/servers/openvpn/certs/alice/network", `{"routes":["192.168.1.0/24"],"lan":[],"full_tunnel":true}`, http.StatusOK, `"full_tunnel":true`},
		{http.MethodGet, "/api/v1/servers/openvpn/certs/alice/network", "", http.StatusOK, `"192.168.1.0/24"`},
		{http.MethodPut, "/api/v1/servers/openvpn/certs/alice/network", `{"routes":["bad"],"lan":[]}`, http.StatusBadRequest, ""},
		{http.MethodPut, "/api/v1/servers/openvpn/named-networks/Office%20LAN", `{"networks":["10.50.0.0/24"],"clients":["alice"]}`, http.StatusOK, `"name":"Office LAN"`},
		{http.MethodGet, "/api/v1/servers/openvpn/named-networks", "", http.StatusOK, `"10.50.0.0/24"`},
		{http.MethodGet, "/api/v1/servers/openvpn/certs/alice/network", "", http.StatusOK, `"named":[{"name":"Office LAN","as":"route"}]`},
		{http.MethodDelete, "/api/v1/servers/openvpn/named-networks/Office%20LAN", "", http.StatusOK, `"networks":[]`},
		{http.MethodDelete, "/api/v1/servers/openvpn/certs/alice", "", http.StatusNoContent, ""},
		{http.MethodGet, "/api/v1/servers/openvpn/certs/alice", "", http.StatusOK, `"status":"revoked"`},
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
		if rec := e.do(http.MethodPost, "/api/v1/servers/openvpn/certs", `{"name":"`+n+`"}`, bearer); rec.Code != http.StatusCreated {
			t.Fatalf("issue %s: %d", n, rec.Code)
		}
	}
	rec := e.do(http.MethodPost, "/api/v1/servers/openvpn/certs/renew", `{"names":["a1","a2","ghost","../x","a1"],"validity_days":90}`, bearer)
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
	if rec := e.do(http.MethodPost, "/api/v1/servers/openvpn/certs/renew", `{"names":[]}`, bearer); rec.Code != http.StatusBadRequest {
		t.Errorf("empty list = %d", rec.Code)
	}
}

func TestServerConfigPassthrough(t *testing.T) {
	e := newTestEnv(t, token)
	bearer := header("Authorization", "Bearer "+token)
	rec := e.do(http.MethodGet, "/api/v1/servers/openvpn/server/config", "", bearer)
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d %s", rec.Code, rec.Body.String())
	}
	cfg := decode[api.ServerConfig](t, rec)
	// The test agent has no server.conf: saving one without ca/cert/key is refused with the problems listed.
	rec = e.do(http.MethodPut, "/api/v1/servers/openvpn/server/config", `{"base_version":"`+cfg.Version+`","raw":"port 1194\n"}`, bearer)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"problems"`) {
		t.Errorf("save invalid = %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(http.MethodGet, "/api/v1/servers/openvpn/server/backups", "", bearer); rec.Code != http.StatusOK {
		t.Errorf("backups = %d", rec.Code)
	}
}

func TestCertDefaults(t *testing.T) {
	e := newTestEnv(t, token)
	bearer := header("Authorization", "Bearer "+token)
	days := func(rec *httptest.ResponseRecorder) int {
		c := decode[api.Cert](t, rec)
		return int(time.Until(c.NotAfter).Hours()/24 + 0.5)
	}
	if rec := e.do(http.MethodGet, "/api/v1/servers/openvpn/cert-defaults", "", bearer); !strings.Contains(rec.Body.String(), `"validity_days":0`) {
		t.Fatalf("default: %s", rec.Body)
	}
	if n := days(e.do(http.MethodPost, "/api/v1/servers/openvpn/certs", `{"name":"a1"}`, bearer)); n < 824 || n > 826 {
		t.Errorf("default lifetime %d days", n)
	}
	for _, bad := range []string{`{"validity_days":-1}`, `{"validity_days":3651}`} {
		if rec := e.do(http.MethodPut, "/api/v1/servers/openvpn/cert-defaults", bad, bearer); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	if rec := e.do(http.MethodPut, "/api/v1/servers/openvpn/cert-defaults", `{"validity_days":3650}`, bearer); rec.Code != http.StatusOK {
		t.Fatalf("set: %d %s", rec.Code, rec.Body)
	}
	// New and renewed certificates get the server's lifetime; an explicit one wins.
	if n := days(e.do(http.MethodPost, "/api/v1/servers/openvpn/certs", `{"name":"a2"}`, bearer)); n < 3649 {
		t.Errorf("new: %d days", n)
	}
	if n := days(e.do(http.MethodPost, "/api/v1/servers/openvpn/certs/a1/renew", "", bearer)); n < 3649 {
		t.Errorf("renewed: %d days", n)
	}
	if n := days(e.do(http.MethodPost, "/api/v1/servers/openvpn/certs", `{"name":"a3","validity_days":30}`, bearer)); n > 31 {
		t.Errorf("explicit: %d days", n)
	}
	if rec := e.do(http.MethodPut, "/api/v1/servers/openvpn/cert-defaults", `{"validity_days":0}`, bearer); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	if n := days(e.do(http.MethodPost, "/api/v1/servers/openvpn/certs", `{"name":"a4"}`, bearer)); n > 826 {
		t.Errorf("back to the default: %d days", n)
	}
}
