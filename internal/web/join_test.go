package web

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shuricksumy/trapdoor/internal/agent"
	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/auth"
	"github.com/shuricksumy/trapdoor/internal/history"
	"github.com/shuricksumy/trapdoor/internal/store"
)

func TestNodeJoin(t *testing.T) {
	ctx := context.Background()
	local := httptest.NewServer(wgAgent(t, "local-secret"))
	t.Cleanup(local.Close)
	node := httptest.NewTLSServer(wgAgent(t, "node-secret"))
	t.Cleanup(node.Close)
	// httptest's servers share one certificate: the second node gets its own.
	dir := t.TempDir()
	cert, otherFP, err := agent.AgentCert(filepath.Join(dir, "agent.crt"), filepath.Join(dir, "agent.key"))
	if err != nil {
		t.Fatal(err)
	}
	other := httptest.NewUnstartedServer(wgAgent(t, "other-secret"))
	other.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	other.StartTLS()
	t.Cleanup(other.Close)
	fp := agent.Fingerprint(node.Certificate().Raw)

	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "td.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	m, err := NewMultiAgent(ctx, agent.NewClient(local.URL, "local-secret", 5*time.Second), db, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	secret := strings.Repeat("s", 32)
	h := New(Deps{Agent: m, Hosts: m, Backup: m, JoinSecret: secret, Stats: &history.Stats{DB: db},
		Auth: auth.New(db, auth.Options{}), APIToken: "tok", Location: time.UTC, Log: log}).Handler()
	call := func(path, body string, authed bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if authed {
			req.Header.Set("Authorization", "Bearer tok")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	join := func(token, name, url, fingerprint, agentToken string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(api.JoinRequest{Token: token, Name: name, URL: url, Fingerprint: fingerprint, AgentToken: agentToken})
		return call("/api/v1/nodes/join", string(b), false)
	}

	// A token needs a login to make; it is shown once.
	if rec := call("/api/v1/nodes/tokens", `{"name":"wg"}`, false); rec.Code != http.StatusUnauthorized {
		t.Errorf("token without login: %d", rec.Code)
	}
	rec := call("/api/v1/nodes/tokens", `{"name":"wg"}`, true)
	var tok api.JoinToken
	if err := json.Unmarshal(rec.Body.Bytes(), &tok); err != nil || rec.Code != http.StatusCreated || !strings.HasPrefix(tok.Token, "tdj_") {
		t.Fatalf("token: %d %s", rec.Code, rec.Body)
	}

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"wrong token":      join("tdj_nope", "wg", node.URL, fp, "node-secret"),
		"token other name": join(tok.Token, "other", other.URL, otherFP, "other-secret"),
	} {
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// The agent must answer with the pinned certificate and its token.
	if rec := join(tok.Token, "wg", node.URL, fp, "wrong-agent-token"); rec.Code != http.StatusBadRequest {
		t.Errorf("wrong agent token: %d %s", rec.Code, rec.Body)
	}
	if rec := join(tok.Token, "wg", node.URL, fp, "node-secret"); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"name":"wg"`) {
		t.Fatalf("join: %d %s", rec.Code, rec.Body)
	}
	if _, err := m.Server("wg.openvpn").ServerInfo(ctx); err != nil {
		t.Errorf("joined node not reachable: %v", err)
	}
	// Used: not for another node; the same node joins again (restart) without it.
	if rec := join(tok.Token, "wg", other.URL, otherFP, "other-secret"); rec.Code != http.StatusUnauthorized {
		t.Errorf("used token, other certificate: %d %s", rec.Code, rec.Body)
	}
	if rec := join("", "wg", node.URL, fp, "node-secret"); rec.Code != http.StatusCreated {
		t.Errorf("rejoin of the same node: %d %s", rec.Code, rec.Body)
	}
	// The compose file's shared secret.
	if rec := join(secret, "other", other.URL, otherFP, "other-secret"); rec.Code != http.StatusCreated {
		t.Errorf("join with the secret: %d %s", rec.Code, rec.Body)
	}
	if hs := m.Hosts(ctx); len(hs) != 2 {
		t.Errorf("nodes = %+v", hs)
	}
	// The old path still works for the list.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if !strings.Contains(rr.Body.String(), `"name":"other"`) {
		t.Errorf("/api/v1/nodes: %s", rr.Body)
	}
}
