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

func TestClientNetwork(t *testing.T) {
	dir := t.TempDir()
	p, err := pki.Init(filepath.Join(dir, "pki"), pki.InitOptions{}, pki.Options{})
	if err != nil {
		t.Fatal(err)
	}
	conf := "server 10.8.0.0 255.255.255.0\ntopology subnet\nclient-config-dir ccd\nroute 192.168.50.0 255.255.255.0\n"
	if err := os.WriteFile(filepath.Join(dir, "server.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	ccdDir := filepath.Join(dir, "ccd")
	cfg := config.Agent{Token: "secret", OpenVPNDir: dir, CCDDir: ccdDir, MgmtAddr: "127.0.0.1:1"}
	srv := httptest.NewServer(serve(New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), p)))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "secret", 10*time.Second).Server(DefaultServerID)
	ctx := context.Background()
	for _, n := range []string{"nas", "phone"} {
		if _, err := c.IssueCert(ctx, api.IssueCertRequest{Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	// 2FA first, then network: both must survive in the same file.
	if _, err := c.UpdateClientAuth(ctx, "nas", api.ClientAuthUpdate{StaticPassword: "pin1"}); err != nil {
		t.Fatal(err)
	}

	got, err := c.SetClientNetwork(ctx, "nas", api.ClientNetwork{
		StaticIP: "10.8.0.50", LAN: []string{"192.168.50.7/24", "192.168.60.0/24"},
		Routes: []string{"10.20.0.0/16", "10.20.0.0/16"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.StaticIP != "10.8.0.50" || strings.Join(got.LAN, ",") != "192.168.50.0/24,192.168.60.0/24" || len(got.Routes) != 1 {
		t.Errorf("set = %+v", got)
	}
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "route 192.168.60.0 255.255.255.0") {
		t.Errorf("warnings = %v", got.Warnings)
	}
	data, _ := os.ReadFile(filepath.Join(ccdDir, "nas"))
	for _, want := range []string{"ifconfig-push 10.8.0.50 255.255.255.0", "iroute 192.168.50.0 255.255.255.0", `push "route 10.20.0.0 255.255.0.0"`, "#STATIC_PASS_HASH:"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("ccd/nas missing %q:\n%s", want, data)
		}
	}

	// Static IP must be unique.
	if _, err := c.SetClientNetwork(ctx, "phone", api.ClientNetwork{StaticIP: "10.8.0.50"}); !IsStatus(err, http.StatusBadRequest) {
		t.Errorf("duplicate static IP: err = %v", err)
	}
	for _, bad := range []api.ClientNetwork{{StaticIP: "10.9.0.1"}, {Routes: []string{"nope"}}, {LAN: []string{"10.8.0.0/16"}}} {
		if _, err := c.SetClientNetwork(ctx, "phone", bad); !IsStatus(err, http.StatusBadRequest) {
			t.Errorf("%+v: err = %v", bad, err)
		}
	}
	if _, err := c.SetClientNetwork(ctx, "ghost", api.ClientNetwork{}); !IsStatus(err, http.StatusNotFound) {
		t.Errorf("unknown client: err = %v", err)
	}

	sn, err := c.ServerNetwork(ctx)
	if err != nil || sn.Subnet != "10.8.0.0/24" || sn.ServerIP != "10.8.0.1" || sn.Topology != "subnet" ||
		!sn.ClientConfigDir || sn.StaticIPs["nas"] != "10.8.0.50" {
		t.Errorf("server network = %+v, %v", sn, err)
	}
	back, err := c.ClientNetwork(ctx, "nas")
	if err != nil || back.StaticIP != "10.8.0.50" || len(back.Warnings) != 1 {
		t.Errorf("get = %+v, %v", back, err)
	}

	// Clearing keeps the 2FA lines.
	if _, err := c.SetClientNetwork(ctx, "nas", api.ClientNetwork{}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(ccdDir, "nas"))
	if strings.Contains(string(data), "ifconfig-push") || !strings.Contains(string(data), "#STATIC_PASS_HASH:") {
		t.Errorf("after clear:\n%s", data)
	}
}
