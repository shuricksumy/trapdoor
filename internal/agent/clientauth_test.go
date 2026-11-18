package agent

import (
	"context"
	"encoding/base32"
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
	"github.com/shuricksumy/trapdoor/internal/clientauth"
	"github.com/shuricksumy/trapdoor/internal/config"
	"github.com/shuricksumy/trapdoor/internal/pki"
)

func authClient(t *testing.T) (*Client, string) {
	t.Helper()
	dir := t.TempDir()
	p, err := pki.Init(filepath.Join(dir, "pki"), pki.InitOptions{}, pki.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ccd := filepath.Join(dir, "ccd")
	cfg := config.Agent{Token: "secret", OpenVPNDir: dir, CCDDir: ccd, TOTPIssuer: "Home VPN", MgmtAddr: "127.0.0.1:1"}
	srv := httptest.NewServer(New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), p).Handler())
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "secret", 10*time.Second), ccd
}

func TestClientAuthLifecycle(t *testing.T) {
	c, ccd := authClient(t)
	ctx := context.Background()
	if _, err := c.IssueCert(ctx, api.IssueCertRequest{Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	// An existing ccd file with a route and an openvpn-ui static password.
	if err := os.MkdirAll(ccd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ccd, "alice"), []byte("ifconfig-push 10.8.0.9 255.255.255.0\n#STATIC_PASS:old\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	st, err := c.ClientAuth(ctx, "alice")
	if err != nil || st.TOTP || !st.Static || !st.LegacyStatic || st.Secret != "" {
		t.Fatalf("initial = %+v, %v", st, err)
	}

	en, err := c.UpdateClientAuth(ctx, "alice", api.ClientAuthUpdate{TOTP: "enable", StaticPassword: "newpin"})
	if err != nil {
		t.Fatal(err)
	}
	if !en.TOTP || en.LegacyStatic || en.Secret == "" || !strings.HasPrefix(en.URI, "otpauth://totp/Home%20VPN:alice?") {
		t.Fatalf("enable = %+v", en)
	}
	data, _ := os.ReadFile(filepath.Join(ccd, "alice"))
	if !strings.Contains(string(data), "ifconfig-push 10.8.0.9") || strings.Contains(string(data), "#STATIC_PASS:old") {
		t.Errorf("ccd after enable:\n%s", data)
	}
	if fi, _ := os.Stat(filepath.Join(ccd, "alice")); fi.Mode().Perm() != 0o640 {
		t.Errorf("file mode changed to %v", fi.Mode().Perm())
	}

	// The secret from the API logs in through auth-verify.
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(en.Secret)
	if err != nil {
		t.Fatal(err)
	}
	creds := filepath.Join(t.TempDir(), "creds")
	_ = os.WriteFile(creds, []byte("alice\nnewpin"+clientauth.Code(key, time.Now())+"\n"), 0o600)
	if _, err := clientauth.VerifyFile(ccd, creds, "alice", time.Now()); err != nil {
		t.Errorf("login with new secret: %v", err)
	}

	// enable again keeps the key; reset makes a new one.
	again, _ := c.UpdateClientAuth(ctx, "alice", api.ClientAuthUpdate{TOTP: "enable"})
	if again.Secret != "" {
		t.Error("enable on an enabled client must not replace the key")
	}
	reset, _ := c.UpdateClientAuth(ctx, "alice", api.ClientAuthUpdate{TOTP: "reset"})
	if reset.Secret == "" || reset.Secret == en.Secret {
		t.Error("reset must create a new key")
	}

	off, err := c.UpdateClientAuth(ctx, "alice", api.ClientAuthUpdate{TOTP: "disable", RemoveStatic: true})
	if err != nil || off.TOTP || off.Static {
		t.Fatalf("disable = %+v, %v", off, err)
	}
	data, _ = os.ReadFile(filepath.Join(ccd, "alice"))
	if string(data) != "ifconfig-push 10.8.0.9 255.255.255.0\n" {
		t.Errorf("ccd after disable = %q", data)
	}

	// Deleting the client deletes its ccd file.
	if err := c.RemoveCert(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ccd, "alice")); !os.IsNotExist(err) {
		t.Error("ccd file not removed with the client")
	}
}

func TestClientAuthValidation(t *testing.T) {
	c, _ := authClient(t)
	ctx := context.Background()
	if _, err := c.IssueCert(ctx, api.IssueCertRequest{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		req  api.ClientAuthUpdate
		want int
	}{
		{"bob", api.ClientAuthUpdate{TOTP: "maybe"}, http.StatusBadRequest},
		{"bob", api.ClientAuthUpdate{StaticPassword: "ab"}, http.StatusBadRequest},
		{"bob", api.ClientAuthUpdate{StaticPassword: "abcd", RemoveStatic: true}, http.StatusBadRequest},
		{"nobody", api.ClientAuthUpdate{TOTP: "enable"}, http.StatusNotFound},
	} {
		if _, err := c.UpdateClientAuth(ctx, tc.name, tc.req); !IsStatus(err, tc.want) {
			t.Errorf("%s %+v: err = %v, want %d", tc.name, tc.req, err, tc.want)
		}
	}
	if _, err := c.RevokeCert(ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateClientAuth(ctx, "bob", api.ClientAuthUpdate{TOTP: "enable"}); !IsStatus(err, http.StatusConflict) {
		t.Errorf("enable on revoked: err = %v, want 409", err)
	}
}
