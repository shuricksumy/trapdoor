package agent

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/config"
	"github.com/shuricksumy/trapdoor/internal/httpx"
)

func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := config.Agent{Token: "secret", OpenVPNDir: "/ovpn", PKIDir: "/ovpn/pki"}
	srv := httptest.NewServer(serve(New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)))
	t.Cleanup(srv.Close)
	return srv
}

func TestHealthzNoToken(t *testing.T) {
	srv := testServer(t)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestHealthCommand(t *testing.T) {
	srv := testServer(t)
	if err := NewClient(srv.URL, "", time.Second).Health(context.Background()); err != nil {
		t.Errorf("health: %v", err)
	}
	if err := NewClient("http://127.0.0.1:1", "", time.Second).Health(context.Background()); err == nil {
		t.Error("health of a dead address must fail")
	}
}

func TestV1RequiresToken(t *testing.T) {
	srv := testServer(t)
	for _, tok := range []string{"", "wrong"} {
		c := NewClient(srv.URL, tok, time.Second)
		if _, err := c.Info(context.Background()); err == nil {
			t.Errorf("token %q: expected error, got nil", tok)
		}
	}
}

func TestClientInfo(t *testing.T) {
	srv := testServer(t)
	info, err := NewClient(srv.URL, "secret", time.Second).Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Servers) != 1 {
		t.Fatalf("servers = %+v", info.Servers)
	}
	if s := info.Servers[0]; s.ID != DefaultServerID || s.Type != api.TypeOpenVPN || s.PKIDir != "/ovpn/pki" || s.OpenVPNDir != "/ovpn" {
		t.Fatalf("unexpected server: %+v", s)
	}
}

func TestUnknownServer(t *testing.T) {
	srv := testServer(t)
	c := NewClient(srv.URL, "secret", time.Second)
	if _, err := c.Server("nope").ListCerts(context.Background()); !IsStatus(err, http.StatusNotFound) {
		t.Errorf("unknown server: err = %v, want 404", err)
	}
	if _, err := c.Server("nope").ServerInfo(context.Background()); !IsStatus(err, http.StatusNotFound) {
		t.Errorf("unknown server info: err = %v, want 404", err)
	}
	info, err := c.Server(DefaultServerID).ServerInfo(context.Background())
	if err != nil || info.ID != DefaultServerID {
		t.Errorf("server info = %+v, %v", info, err)
	}
}

func TestClientOverUnixSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "agent.sock")
	l, err := httpx.Listen("unix:" + sock)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Agent{Token: "secret", PKIDir: "/p"}
	h := serve(New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- httpx.Serve(ctx, l, h) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})

	info, err := NewClient("unix:"+sock, "secret", time.Second).Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Servers) != 1 || info.Servers[0].PKIDir != "/p" {
		t.Fatalf("servers = %+v, want PKIDir /p", info.Servers)
	}
}
