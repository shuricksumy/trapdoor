package agent

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/config"
)

// Each server says which container runs it, and the machine says where new
// servers of each type would go.
func TestServerContainers(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Agent{Token: "secret", OpenVPNDir: dir, PKIDir: filepath.Join(dir, "pki"), CCDDir: filepath.Join(dir, "ccd"),
		MgmtAddr: "127.0.0.1:1", OpenVPNServersDir: filepath.Join(dir, "servers"), WireGuard: "file",
		WireGuardDir: t.TempDir(), AmneziaWGDir: t.TempDir(),
		ContainerOpenVPN: "vpn-openvpn", ContainerWireGuard: "vpn-wireguard", ContainerAmneziaWG: "vpn-amneziawg"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewHost("secret", log)
	h.EnableOpenVPN(cfg, []*Server{New(cfg, log, nil)})
	h.EnableWireGuard(cfg, nil)
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "secret", 5*time.Second)
	ctx := context.Background()
	for _, req := range []api.CreateServer{
		{Type: api.TypeWireGuard, ID: "wg0", PublicHost: "vpn.example.com"},
		{Type: api.TypeAmneziaWG, ID: "awg0", Port: 51830, PublicHost: "vpn.example.com", Subnet: "10.50.0.0/24"},
		{Type: api.TypeOpenVPN, ID: "ovpn-tcp", Port: 443, Proto: "tcp"},
	} {
		if _, err := c.CreateServer(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	info, err := c.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"openvpn": "vpn-openvpn", "ovpn-tcp": "vpn-openvpn", "wg0": "vpn-wireguard", "awg0": "vpn-amneziawg"}
	for _, s := range info.Servers {
		if s.Container != want[s.ID] {
			t.Errorf("%s runs in %q, want %q", s.ID, s.Container, want[s.ID])
		}
	}
	if info.Containers[api.TypeOpenVPN] != "vpn-openvpn" || info.Containers[api.TypeWireGuard] != "vpn-wireguard" || info.Containers[api.TypeAmneziaWG] != "vpn-amneziawg" {
		t.Errorf("containers = %v", info.Containers)
	}

	// An agent without WireGuard offers only OpenVPN.
	h2 := NewHost("secret", log)
	h2.EnableOpenVPN(cfg, []*Server{New(cfg, log, nil)})
	srv2 := httptest.NewServer(h2.Handler())
	t.Cleanup(srv2.Close)
	info, _ = NewClient(srv2.URL, "secret", 5*time.Second).Info(ctx)
	if _, ok := info.Containers[api.TypeWireGuard]; ok || len(info.Containers) != 1 {
		t.Errorf("containers without WireGuard = %v", info.Containers)
	}
}
