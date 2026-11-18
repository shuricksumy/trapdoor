package agent

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/config"
	"github.com/shuricksumy/trapdoor/internal/pki"
)

func certClient(t *testing.T, withPKI bool) *Client {
	t.Helper()
	var p pki.PKI
	if withPKI {
		s, err := pki.Init(filepath.Join(t.TempDir(), "pki"), pki.InitOptions{}, pki.Options{})
		if err != nil {
			t.Fatal(err)
		}
		p = s
	}
	cfg := config.Agent{Token: "secret"}
	srv := httptest.NewServer(New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), p).Handler())
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "secret", 10*time.Second)
}

func TestCertLifecycleOverHTTP(t *testing.T) {
	c := certClient(t, true)
	ctx := context.Background()

	issued, err := c.IssueCert(ctx, api.IssueCertRequest{Name: "alice", ValidityDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	if issued.Status != api.CertValid || issued.NotAfter.Before(time.Now().Add(29*24*time.Hour)) {
		t.Fatalf("issued = %+v", issued)
	}
	if _, err := c.IssueCert(ctx, api.IssueCertRequest{Name: "alice"}); !IsStatus(err, http.StatusConflict) {
		t.Errorf("duplicate: err = %v, want 409", err)
	}

	rev, err := c.RevokeCert(ctx, "alice")
	if err != nil || rev.Status != api.CertRevoked {
		t.Fatalf("revoke: %+v, %v", rev, err)
	}
	if _, err := c.RevokeCert(ctx, "alice"); !IsStatus(err, http.StatusConflict) {
		t.Errorf("double revoke: err = %v, want 409", err)
	}
	unrev, err := c.UnrevokeCert(ctx, "alice")
	if err != nil || unrev.Status != api.CertValid {
		t.Fatalf("unrevoke: %+v, %v", unrev, err)
	}
	renewed, err := c.RenewCert(ctx, "alice", 0)
	if err != nil || renewed.Serial == issued.Serial {
		t.Fatalf("renew: %+v, %v", renewed, err)
	}

	list, err := c.ListCerts(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %+v, %v", list, err)
	}
	if err := c.GenCRL(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveCert(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetCert(ctx, "alice")
	if err != nil || got.Status != api.CertRevoked {
		t.Fatalf("after remove: %+v, %v", got, err)
	}
	if _, err := c.GetCert(ctx, "nobody"); !IsStatus(err, http.StatusNotFound) {
		t.Errorf("unknown: err = %v, want 404", err)
	}
}

func TestCertValidation(t *testing.T) {
	c := certClient(t, true)
	ctx := context.Background()
	for _, req := range []api.IssueCertRequest{
		{Name: "../etc/passwd"},
		{Name: "a b"},
		{Name: "ok", Passphrase: "abc"},
		{Name: "ok", ValidityDays: -1},
		{Name: "ok", ValidityDays: 99999},
		{Name: "ok", Kind: "intermediate"},
	} {
		if _, err := c.IssueCert(ctx, req); err == nil || IsStatus(err, http.StatusInternalServerError) {
			t.Errorf("%+v: err = %v, want 4xx", req, err)
		}
	}
}

func TestCertRawRequests(t *testing.T) {
	c := certClient(t, true)
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"name":"x","unknown":1}`, http.StatusBadRequest},
		{`not json`, http.StatusBadRequest},
		{`{"name":"x"}`, http.StatusCreated},
	} {
		req, _ := http.NewRequest(http.MethodPost, c.base+"/v1/certs", strings.NewReader(tc.body))
		req.Header.Set(api.HeaderToken, "secret")
		resp, err := c.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s: status %d, want %d", tc.body, resp.StatusCode, tc.want)
		}
	}
}

func TestCertsWithoutPKI(t *testing.T) {
	c := certClient(t, false)
	if _, err := c.ListCerts(context.Background()); !IsStatus(err, http.StatusServiceUnavailable) {
		t.Errorf("err = %v, want 503", err)
	}
	info, err := c.Info(context.Background())
	if err != nil || info.PKIReady {
		t.Errorf("info = %+v, %v", info, err)
	}
}

func TestProfileDownload(t *testing.T) {
	ovpnDir := t.TempDir()
	s, err := pki.Init(filepath.Join(ovpnDir, "easy-rsa/pki"), pki.InitOptions{}, pki.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Agent{Token: "secret", OpenVPNDir: ovpnDir}
	srv := httptest.NewServer(New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), s).Handler())
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "secret", 10*time.Second)
	ctx := context.Background()

	if _, err := c.IssueCert(ctx, api.IssueCertRequest{Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Profile(ctx, "alice"); !IsStatus(err, http.StatusServiceUnavailable) {
		t.Errorf("no template: err = %v, want 503", err)
	}

	for name, content := range map[string]string{
		"client-template.txt": "client\nremote vpn.example.com 1194\n",
		"server.conf":         "tls-crypt tc.key\n",
		"tc.key":              "TC-KEY\n",
	} {
		if err := os.WriteFile(filepath.Join(ovpnDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	profile, err := c.Profile(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"remote vpn.example.com 1194", "<cert>", "BEGIN PRIVATE KEY", "<tls-crypt>\nTC-KEY"} {
		if !strings.Contains(string(profile), want) {
			t.Errorf("profile missing %q", want)
		}
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/certs/alice/ovpn", nil)
	req.Header.Set(api.HeaderToken, "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if cd := resp.Header.Get("Content-Disposition"); cd != `attachment; filename="alice.ovpn"` {
		t.Errorf("Content-Disposition = %q", cd)
	}

	if _, err := c.RevokeCert(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Profile(ctx, "alice"); !IsStatus(err, http.StatusConflict) {
		t.Errorf("revoked: err = %v, want 409", err)
	}
	if _, err := c.Profile(ctx, "nobody"); !IsStatus(err, http.StatusNotFound) {
		t.Errorf("unknown: err = %v, want 404", err)
	}
}
