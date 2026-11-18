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

	"github.com/shuricksumy/tunnelward/internal/config"
	"github.com/shuricksumy/tunnelward/internal/httpx"
)

func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := config.Agent{Token: "secret", OpenVPNDir: "/ovpn", PKIDir: "/ovpn/pki"}
	srv := httptest.NewServer(New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
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
	if info.PKIDir != "/ovpn/pki" || info.OpenVPNDir != "/ovpn" {
		t.Fatalf("unexpected info: %+v", info)
	}
}

func TestClientOverUnixSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "agent.sock")
	l, err := httpx.Listen("unix:" + sock)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Agent{Token: "secret", PKIDir: "/p"}
	h := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()

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
	if info.PKIDir != "/p" {
		t.Fatalf("PKIDir = %q, want /p", info.PKIDir)
	}
}
