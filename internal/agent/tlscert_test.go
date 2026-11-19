package agent

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAgentCertAndPinning(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "a.crt"), filepath.Join(dir, "a.key")
	c, fp, err := AgentCert(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(key); fi.Mode().Perm() != 0o600 {
		t.Errorf("key mode %v", fi.Mode().Perm())
	}
	if _, fp2, _ := AgentCert(cert, key); fp2 != fp {
		t.Error("the certificate changed on the second start")
	}
	if n, err := NormalizeFingerprint(strings.ToUpper(strings.TrimPrefix(fp, "sha256:"))); err != nil || n != fp {
		t.Errorf("normalize = %q, %v", n, err)
	}
	if _, err := NormalizeFingerprint("sha256:abcd"); err == nil {
		t.Error("short fingerprint accepted")
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewUnstartedServer(NewHost("secret", log).Handler())
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{c}, MinVersion: tls.VersionTLS13}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	ctx := context.Background()
	if _, err := NewPinnedClient(srv.URL, "secret", fp, 5*time.Second).Info(ctx); err != nil {
		t.Errorf("pinned: %v", err)
	}
	other := "sha256:" + strings.Repeat("0", 64)
	if _, err := NewPinnedClient(srv.URL, "secret", other, 5*time.Second).Info(ctx); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("wrong pin: %v", err)
	}
	if err := NewInsecureHealthClient(srv.URL, 5*time.Second).Health(ctx); err != nil {
		t.Errorf("health: %v", err)
	}
}
