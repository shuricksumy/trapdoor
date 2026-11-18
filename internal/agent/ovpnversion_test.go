package agent

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shuricksumy/trapdoor/internal/ccdnet"
	"github.com/shuricksumy/trapdoor/internal/config"
)

func TestTopologyFor(t *testing.T) {
	for out, want := range map[string]ccdnet.Topology{
		"OpenVPN 2.6.20 aarch64-alpine-linux-musl [SSL (OpenSSL)]": ccdnet.TopologyNet30,
		"OpenVPN 2.7.7 x86_64-alpine-linux-musl [SSL (OpenSSL)]":   ccdnet.TopologySubnet,
		"OpenVPN 2.10.0 x86_64":                                    ccdnet.TopologySubnet,
		"OpenVPN 3.0.0":                                            ccdnet.TopologySubnet,
		"":                                                         ccdnet.TopologyNet30,
		"something else":                                           ccdnet.TopologyNet30,
	} {
		if got := topologyFor(out); got != want {
			t.Errorf("topologyFor(%q) = %s, want %s", out, got, want)
		}
	}
}

// With OpenVPN 2.7 a server.conf without a topology line runs in subnet
// mode, so trapdoor must treat it as subnet too (e.g. for fixed addresses).
func TestDefaultTopologyFollowsOpenVPN(t *testing.T) {
	for version, want := range map[string]string{"2.6.20": "net30", "2.7.7": "subnet"} {
		dir := t.TempDir()
		bin := filepath.Join(dir, "openvpn")
		script := "#!/bin/sh\necho 'OpenVPN " + version + " x86_64-alpine-linux-musl [SSL (OpenSSL)]'\n[ \"$1\" = --version ] || exit 1\n"
		if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "server.conf"), []byte("server 10.8.0.0 255.255.255.0\nclient-config-dir ccd\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg := config.Agent{Token: "secret", OpenVPNDir: dir, CCDDir: filepath.Join(dir, "ccd"), MgmtAddr: "127.0.0.1:1", OpenVPNBin: bin}
		srv := httptest.NewServer(serve(New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)))
		c := NewClient(srv.URL, "secret", 10*time.Second).Server(DefaultServerID)
		n, err := c.ServerNetwork(context.Background())
		if err != nil || n.Topology != want {
			t.Errorf("OpenVPN %s: topology = %q, %v; want %s", version, n.Topology, err, want)
		}
		sc, err := c.ServerConfig(context.Background())
		if err != nil || sc.Settings.Topology != want {
			t.Errorf("OpenVPN %s: settings topology = %q, %v; want %s", version, sc.Settings.Topology, err, want)
		}
		srv.Close()
	}
}
