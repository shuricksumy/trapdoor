package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/shuricksumy/trapdoor/internal/api"
)

func TestWireGuardThroughWeb(t *testing.T) {
	e := newTestEnv(t, "")
	_, _ = e.auth.CreateUser(context.Background(), "admin", "long-enough-pw")
	c := sessionCookie(t, e.login("admin", "long-enough-pw"))
	same := header("Origin", "https://vpn.example.com")

	if rec := e.do(http.MethodPost, "/api/v1/servers", `{"type":"wireguard","id":"wg0","public_host":"vpn.example.com"}`, cookie(c), header("Sec-Fetch-Site", "cross-site")); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site create = %d", rec.Code)
	}
	steps := []struct {
		method, path, body string
		want               int
		contains           string
	}{
		{http.MethodPost, "/api/v1/servers", `{"type":"wireguard","id":"wg0","name":"Home","public_host":"vpn.example.com"}`, http.StatusCreated, `"endpoint":"vpn.example.com:51820"`},
		{http.MethodPost, "/api/v1/servers", `{"type":"wireguard","id":"wg0","public_host":"vpn.example.com","port":51821,"subnet":"10.20.0.0/24"}`, http.StatusConflict, "exists"},
		{http.MethodPost, "/api/v1/servers", `{"type":"wireguard","id":"WG","public_host":"h"}`, http.StatusBadRequest, "interface name"},
		{http.MethodPost, "/api/v1/servers/wg0/peers", `{"name":"alice"}`, http.StatusCreated, `"address":"10.9.0.2"`},
		{http.MethodPost, "/api/v1/servers/wg0/peers", `{"name":"alice"}`, http.StatusConflict, ""},
		{http.MethodGet, "/api/v1/servers/wg0/peers", "", http.StatusOK, `"name":"alice"`},
		{http.MethodGet, "/api/v1/servers/wg0/peers/alice", "", http.StatusOK, `"preshared_key":true`},
		{http.MethodPost, "/api/v1/servers/wg0/peers/bulk", `{"prefix":"guest","count":2}`, http.StatusCreated, `"name":"guest-2"`},
		{http.MethodGet, "/api/v1/servers/wg0/settings", "", http.StatusOK, `"keepalive":25`},
		{http.MethodPut, "/api/v1/servers/wg0/settings", `{"name":"Home","public_host":"vpn.example.com","public_port":51820,"dns":["1.1.1.1"],"allowed_ips":["0.0.0.0/0"],"mtu":0,"keepalive":20,"psk":true}`, http.StatusOK, `"keepalive":20`},
		{http.MethodPut, "/api/v1/servers/wg0/peers/alice", `{"settings":{"notes":"phone","dns":["9.9.9.9"]}}`, http.StatusOK, `"notes":"phone"`},
		{http.MethodGet, "/api/v1/servers/wg0/peers/alice/conf", "", http.StatusOK, "Endpoint = vpn.example.com:51820"},
		{http.MethodPut, "/api/v1/servers/wg0/peers/alice", `{"disabled":true}`, http.StatusOK, `"disabled":true`},
		{http.MethodGet, "/api/v1/servers/wg0/peers/alice/conf", "", http.StatusConflict, "disabled"},
		{http.MethodGet, "/api/v1/servers/wg0/peers/a%20b", "", http.StatusBadRequest, "invalid"},
		{http.MethodDelete, "/api/v1/servers/wg0/peers/alice", "", http.StatusNoContent, ""},
		{http.MethodDelete, "/api/v1/servers/openvpn", "", http.StatusConflict, "first OpenVPN server cannot be removed"},
		{http.MethodDelete, "/api/v1/servers/wg0", "", http.StatusNoContent, ""},
		{http.MethodGet, "/api/v1/servers/wg0/peers", "", http.StatusNotFound, "no server"},
	}
	for _, st := range steps {
		rec := e.do(st.method, st.path, st.body, cookie(c), same)
		if rec.Code != st.want || !strings.Contains(rec.Body.String(), st.contains) {
			t.Errorf("%s %s %s: %d %s; want %d containing %q", st.method, st.path, st.body, rec.Code, rec.Body.String(), st.want, st.contains)
		}
	}
}

// A WireGuard client's display name (its notes) is used in messages, like an
// OpenVPN client's; a client without one keeps its config name.
func TestWireGuardDisplayNames(t *testing.T) {
	e := newTestEnv(t, "")
	_, _ = e.auth.CreateUser(context.Background(), "admin", "long-enough-pw")
	c := sessionCookie(t, e.login("admin", "long-enough-pw"))
	same := header("Origin", "https://vpn.example.com")
	for _, st := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/servers", `{"type":"wireguard","id":"wg0","name":"Home","public_host":"vpn.example.com"}`},
		{http.MethodPost, "/api/v1/servers/wg0/peers", `{"name":"anna","notes":"Anna's phone"}`},
		{http.MethodPost, "/api/v1/servers/wg0/peers", `{"name":"bob"}`},
	} {
		if rec := e.do(st.method, st.path, st.body, cookie(c), same); rec.Code != http.StatusCreated {
			t.Fatalf("%s %s: %d %s", st.method, st.path, rec.Code, rec.Body)
		}
	}
	s := &Server{Deps: Deps{Agent: e.agent}}
	for _, typ := range []string{"", api.TypeWireGuard} {
		got := s.displayNames(context.Background(), "wg0", typ)
		if len(got) != 1 || got["anna"] != "Anna's phone" {
			t.Errorf("displayNames(%q) = %v", typ, got)
		}
	}
	if got := s.serverNames(context.Background(), "wg0"); got["wg0/anna"] != "Anna's phone" {
		t.Errorf("serverNames = %v", got)
	}
	if _, ok := s.serverNames(context.Background(), "wg0")["wg0/bob"]; ok {
		t.Error("bob has no display name")
	}
}
