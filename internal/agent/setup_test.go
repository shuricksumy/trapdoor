package agent

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shuricksumy/tunnelward/internal/api"
	"github.com/shuricksumy/tunnelward/internal/config"
)

func TestSetup(t *testing.T) {
	bin, err := exec.LookPath("openvpn")
	if err != nil {
		t.Skip("openvpn not installed")
	}
	dir := t.TempDir()
	cfg := config.Agent{Token: "secret", OpenVPNDir: dir, PKIDir: filepath.Join(dir, "easy-rsa", "pki"),
		CCDDir: filepath.Join(dir, "ccd"), MgmtAddr: "127.0.0.1:2080", OpenVPNBin: bin,
		CRLCopies: []string{filepath.Join(dir, "crl.pem")}}
	s := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	fm := &fakeMgmt{}
	s.mgmt = fm
	s.restartWait = 100 * time.Millisecond
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "secret", 30*time.Second)
	ctx := context.Background()

	st, err := c.SetupStatus(ctx)
	if err != nil || !st.Needed || st.HasPKI || st.Defaults.Port != 1194 {
		t.Fatalf("status = %+v, %v", st, err)
	}
	if _, err := c.Setup(ctx, api.SetupRequest{}); err == nil || !strings.Contains(err.Error(), "public address") {
		t.Errorf("empty host: %v", err)
	}

	res, err := c.Setup(ctx, api.SetupRequest{PublicHost: "vpn.example.com", PublicPort: 443, Proto: "tcp",
		DNS: []string{"1.1.1.1"}, FullTunnel: true, TwoFactor: true, KeyType: "ec384"})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if !res.Running || !strings.Contains(res.ServerConf, "tls-crypt-v2 tls-crypt-v2.key") ||
		!strings.Contains(res.ServerConf, "ca easy-rsa/pki/ca.crt") || !strings.Contains(res.ServerConf, "crl-verify crl.pem") ||
		!strings.Contains(res.ServerConf, "auth-verify --ccd "+cfg.CCDDir) || !strings.Contains(res.ServerConf, "user nobody") {
		t.Errorf("result = %+v", res)
	}
	for _, f := range []string{"server.conf", "client-template.txt", "tls-crypt-v2.key", "crl.pem", "ccd"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	if s.PKI() == nil {
		t.Fatal("PKI not switched on")
	}
	// The server certificate is not a client, and a client profile builds.
	if _, err := c.IssueCert(ctx, api.IssueCertRequest{Name: "alice"}); err != nil {
		t.Fatalf("issue: %v", err)
	}
	prof, err := c.Profile(ctx, "alice")
	if err != nil || !strings.Contains(string(prof), "remote vpn.example.com 443") || !strings.Contains(string(prof), "<tls-crypt-v2>") {
		t.Errorf("profile: %v\n%s", err, prof)
	}

	if _, err := c.Setup(ctx, api.SetupRequest{PublicHost: "vpn.example.com"}); err == nil || !strings.Contains(err.Error(), "already set up") {
		t.Errorf("second setup: %v", err)
	}
	if st, _ := c.SetupStatus(ctx); st.Needed || !st.HasPKI || st.CAName != "tunnelward CA" {
		t.Errorf("status after = %+v", st)
	}

	// OpenVPN not answering is not a failure: the config is written.
	dir2 := t.TempDir()
	cfg.OpenVPNDir, cfg.PKIDir, cfg.CCDDir, cfg.CRLCopies = dir2, filepath.Join(dir2, "pki"), filepath.Join(dir2, "ccd"), nil
	s2 := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	s2.mgmt = &fakeMgmt{down: true}
	s2.restartWait = 100 * time.Millisecond
	srv2 := httptest.NewServer(s2.Handler())
	t.Cleanup(srv2.Close)
	res, err = NewClient(srv2.URL, "secret", 30*time.Second).Setup(ctx, api.SetupRequest{PublicHost: "203.0.113.5", TLS: "tls-auth"})
	if err != nil || res.Running || !strings.Contains(res.ServerConf, "tls-auth ta.key 0") || !strings.Contains(res.ServerConf, "crl-verify pki/crl.pem") ||
		strings.Contains(res.ServerConf, "user nobody") || len(res.Warnings) == 0 {
		t.Errorf("not running: %+v, %v", res, err)
	}
}

func TestSetupFixedPort(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Agent{Token: "secret", OpenVPNDir: dir, PKIDir: filepath.Join(dir, "pki"), CCDDir: filepath.Join(dir, "ccd"),
		MgmtAddr: "127.0.0.1:2080", SetupPort: 1194, SetupProto: "udp", SetupPublicPort: 11194}
	s := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "secret", 5*time.Second)
	ctx := context.Background()
	st, err := c.SetupStatus(ctx)
	if err != nil || st.FixedPort != 1194 || st.FixedProto != "udp" || st.Defaults.PublicPort != 11194 || st.Defaults.Port != 1194 {
		t.Fatalf("status = %+v, %v", st, err)
	}
	for _, req := range []api.SetupRequest{{PublicHost: "vpn.example.com", Port: 1195}, {PublicHost: "vpn.example.com", Proto: "tcp"}} {
		if _, err := c.Setup(ctx, req); err == nil || !strings.Contains(err.Error(), "container") {
			t.Errorf("%+v: %v", req, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "server.conf")); err == nil {
		t.Error("server.conf written")
	}
}
