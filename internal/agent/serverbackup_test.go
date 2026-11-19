package agent

import (
	"bytes"
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

func fetchBackup(t *testing.T, c *Client) []byte {
	t.Helper()
	rc, err := c.ServerBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestServerBackupWireGuard(t *testing.T) {
	c, _, _ := ovpnHost(t) // WireGuard in file mode
	ctx := context.Background()
	if _, err := c.CreateServer(ctx, api.CreateServer{Type: api.TypeAmneziaWG, ID: "awg0", Port: 51830, PublicHost: "vpn.example.com", Subnet: "10.50.0.0/24"}); err != nil {
		t.Fatal(err)
	}
	wgc := c.Server("awg0")
	if _, err := wgc.AddPeer(ctx, api.NewPeer{Name: "phone"}); err != nil {
		t.Fatal(err)
	}
	before, _ := wgc.ServerInfo(ctx)
	peerBefore, _ := wgc.Peer(ctx, "phone")
	tarball := fetchBackup(t, wgc)

	// The server exists: restore needs replace.
	if _, err := c.RestoreServer(ctx, bytes.NewReader(tarball), false); !IsStatus(err, http.StatusConflict) {
		t.Errorf("restore over an existing server: %v", err)
	}
	if err := wgc.RemoveServer(ctx); err != nil {
		t.Fatal(err)
	}
	info, err := c.RestoreServer(ctx, bytes.NewReader(tarball), false)
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != "awg0" || info.PublicKey != before.PublicKey || info.Obfuscation == nil || info.Obfuscation.H1 != before.Obfuscation.H1 {
		t.Errorf("restored = %+v, before %+v", info, before)
	}
	if p, err := wgc.Peer(ctx, "phone"); err != nil || p.PublicKey != peerBefore.PublicKey || p.Address != peerBefore.Address {
		t.Errorf("peer after restore = %+v, %v", p, err)
	}
	// With replace it swaps the running one.
	if _, err := c.RestoreServer(ctx, bytes.NewReader(tarball), true); err != nil {
		t.Errorf("restore with replace: %v", err)
	}
	if _, err := c.RestoreServer(ctx, strings.NewReader("not a tar"), false); !IsStatus(err, http.StatusBadRequest) {
		t.Errorf("garbage: %v", err)
	}
}

func TestServerBackupOpenVPN(t *testing.T) {
	c, h, cfg := ovpnHost(t)
	ctx := context.Background()
	if _, err := c.CreateServer(ctx, api.CreateServer{Type: api.TypeOpenVPN, ID: "ovpn-a", Port: 443, Proto: "tcp"}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cfg.OpenVPNServersDir, "ovpn-a")
	conf := "port 443\nproto tcp-server\ndev ovpn-a\ndev-type tun\nserver 10.9.0.0 255.255.255.0\nmanagement 127.0.0.1 2081 management.pw\n"
	if err := os.WriteFile(filepath.Join(dir, "server.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := pki.Init(filepath.Join(dir, "pki"), pki.InitOptions{CommonName: "A CA"}, pki.Options{}); err != nil {
		t.Fatal(err)
	}
	m, _ := h.get("ovpn-a")
	m.b.(*Server).mgmt = &fakeMgmt{}
	tarball := fetchBackup(t, c.Server("ovpn-a"))
	if err := c.Server("ovpn-a").RemoveServer(ctx); err != nil {
		t.Fatal(err)
	}
	// Another server takes the management port 2081 meanwhile.
	if _, err := c.CreateServer(ctx, api.CreateServer{Type: api.TypeOpenVPN, ID: "ovpn-b", Port: 1196, Proto: "udp"}); err != nil {
		t.Fatal(err)
	}
	info, err := c.RestoreServer(ctx, bytes.NewReader(tarball), false)
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != "ovpn-a" || !info.PKIReady || info.CAName != "A CA" || info.ListenPort != 443 {
		t.Errorf("restored = %+v", info)
	}
	if got := read(t, filepath.Join(dir, "server.conf")); !strings.Contains(got, "management 127.0.0.1 2082 management.pw\n") || !strings.Contains(got, "port 443\n") {
		t.Errorf("server.conf after restore:\n%s", got)
	}
	if !strings.Contains(read(t, filepath.Join(dir, ovpnStateFile)), "127.0.0.1:2082") {
		t.Error("state keeps the taken management port")
	}
	if left, _ := filepath.Glob(filepath.Join(cfg.OpenVPNServersDir, ".restore-*")); len(left) != 0 {
		t.Errorf("staging left: %v", left)
	}
	// Its port is taken by another server now: refused, nothing changes.
	if err := c.Server("ovpn-a").RemoveServer(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateServer(ctx, api.CreateServer{Type: api.TypeOpenVPN, ID: "ovpn-c", Port: 443, Proto: "tcp"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RestoreServer(ctx, bytes.NewReader(tarball), false); !IsStatus(err, http.StatusConflict) {
		t.Errorf("restore onto a used port: %v", err)
	}
}

func TestServerBackupFirstOpenVPN(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	newAgent := func() (*Client, *Server, config.Agent) {
		dir := t.TempDir()
		cfg := config.Agent{Token: "secret", OpenVPNDir: dir, PKIDir: filepath.Join(dir, "easy-rsa/pki"), CCDDir: filepath.Join(dir, "ccd"),
			MgmtAddr: "127.0.0.1:2080", OpenVPNServersDir: filepath.Join(dir, "servers")}
		s := New(cfg, log, nil)
		s.mgmt = &fakeMgmt{}
		h := NewHost("secret", log, s)
		h.EnableOpenVPN(cfg, nil)
		srv := httptest.NewServer(h.Handler())
		t.Cleanup(srv.Close)
		return NewClient(srv.URL, "secret", 10*time.Second), s, cfg
	}
	c1, _, cfg1 := newAgent()
	if err := os.WriteFile(filepath.Join(cfg1.OpenVPNDir, "server.conf"), []byte("server 10.8.0.0 255.255.255.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"trapdoor-agent.key", "servers/ovpn-x/server.conf", "ccd/alice"} {
		p := filepath.Join(cfg1.OpenVPNDir, f)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(f), 0o600)
	}
	if _, err := pki.Init(cfg1.PKIDir, pki.InitOptions{CommonName: "First CA"}, pki.Options{}); err != nil {
		t.Fatal(err)
	}
	tarball := fetchBackup(t, c1.Server(DefaultServerID))
	for _, leak := range []string{"trapdoor-agent.key", "servers/ovpn-x"} {
		if bytes.Contains(tarball, []byte(leak)) {
			t.Errorf("%s is in the first server's backup", leak)
		}
	}

	// Onto a machine where OpenVPN is not set up yet.
	c2, s2, cfg2 := newAgent()
	info, err := c2.RestoreServer(ctx, bytes.NewReader(tarball), false)
	if err != nil {
		t.Fatal(err)
	}
	if !info.PKIReady || info.CAName != "First CA" || s2.PKI() == nil {
		t.Errorf("restored = %+v", info)
	}
	if read(t, filepath.Join(cfg2.CCDDir, "alice")) != "ccd/alice" {
		t.Error("ccd not restored")
	}
	// Set up now: only with replace.
	if _, err := c2.RestoreServer(ctx, bytes.NewReader(tarball), false); !IsStatus(err, http.StatusConflict) {
		t.Errorf("restore over a set-up server: %v", err)
	}
	if _, err := c2.RestoreServer(ctx, bytes.NewReader(tarball), true); err != nil {
		t.Errorf("restore with replace: %v", err)
	}
}
