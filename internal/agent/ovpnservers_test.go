package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/config"
)

// ovpnHost is a host with the first OpenVPN server (10.8.0.0/24, UDP 1194)
// that can add more; WireGuard in file mode.
func ovpnHost(t *testing.T) (*Client, *Host, config.Agent) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "server.conf"), []byte("port 1194\nproto udp\nserver 10.8.0.0 255.255.255.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Agent{Token: "secret", OpenVPNDir: dir, PKIDir: filepath.Join(dir, "pki"), CCDDir: filepath.Join(dir, "ccd"),
		MgmtAddr: "127.0.0.1:2080", OpenVPNServersDir: filepath.Join(dir, "servers"), Firewall: FirewallFile,
		WireGuard: "file", WireGuardDir: t.TempDir()}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewHost("secret", log, New(cfg, log, nil))
	extra, err := LoadOpenVPN(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	h.EnableOpenVPN(cfg, extra)
	h.EnableWireGuard(cfg, nil)
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "secret", 30*time.Second), h, cfg
}

func TestAddOpenVPNServer(t *testing.T) {
	c, h, cfg := ovpnHost(t)
	ctx := context.Background()

	info, err := c.CreateServer(ctx, api.CreateServer{Type: api.TypeOpenVPN, ID: "ovpn-tcp", Name: "TCP 443", Port: 443, Proto: "tcp"})
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != "ovpn-tcp" || info.Type != api.TypeOpenVPN || info.Name != "TCP 443" || info.PKIReady || info.ListenPort != 443 || info.Proto != "tcp" {
		t.Errorf("info = %+v", info)
	}
	var st ovpnState
	if err := json.Unmarshal([]byte(read(t, filepath.Join(cfg.OpenVPNServersDir, "ovpn-tcp", ovpnStateFile))), &st); err != nil || st.Mgmt != "127.0.0.1:2081" {
		t.Errorf("state = %+v, %v", st, err)
	}

	for name, req := range map[string]api.CreateServer{
		"same id":         {Type: api.TypeOpenVPN, ID: "ovpn-tcp", Port: 1195, Proto: "udp"},
		"first's port":    {Type: api.TypeOpenVPN, ID: "ovpn2", Port: 1194, Proto: "udp"},
		"second's port":   {Type: api.TypeOpenVPN, ID: "ovpn2", Port: 443, Proto: "tcp"},
		"wireguard there": {Type: api.TypeWireGuard, ID: "wg0", Port: 1194, PublicHost: "vpn.example.com"},
	} {
		if _, err := c.CreateServer(ctx, req); !IsStatus(err, http.StatusConflict) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, req := range map[string]api.CreateServer{
		"tun id":     {Type: api.TypeOpenVPN, ID: "tun1", Port: 1195, Proto: "udp"},
		"first's id": {Type: api.TypeOpenVPN, ID: DefaultServerID, Port: 1195, Proto: "udp"},
		"no port":    {Type: api.TypeOpenVPN, ID: "ovpn2", Proto: "udp"},
		"bad proto":  {Type: api.TypeOpenVPN, ID: "ovpn2", Port: 1195, Proto: "sctp"},
	} {
		if _, err := c.CreateServer(ctx, req); !IsStatus(err, http.StatusBadRequest) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// UDP 443 is free next to TCP 443; it gets the next management port.
	if _, err := c.CreateServer(ctx, api.CreateServer{Type: api.TypeOpenVPN, ID: "ovpn-udp", Port: 443, Proto: "udp"}); err != nil {
		t.Errorf("UDP next to TCP: %v", err)
	}
	if m, _ := h.get("ovpn-udp"); m.b.(*Server).cfg.MgmtAddr != "127.0.0.1:2082" {
		t.Errorf("management = %s", m.b.(*Server).cfg.MgmtAddr)
	}

	// The wizard keeps the port and suggests a subnet nobody uses.
	sc := c.Server("ovpn-tcp")
	ss, err := sc.SetupStatus(ctx)
	if err != nil || !ss.Needed || ss.FixedPort != 443 || ss.FixedProto != "tcp" || ss.Defaults.Subnet != "10.9.0.0/24" {
		t.Errorf("setup status = %+v, %v", ss, err)
	}
	if _, err := sc.Setup(ctx, api.SetupRequest{PublicHost: "vpn.example.com", Subnet: "10.8.0.0/16"}); !IsStatus(err, http.StatusConflict) {
		t.Errorf("setup on the first server's subnet: %v", err)
	}

	// Listed after a restart of the agent.
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	again, err := LoadOpenVPN(cfg, log)
	if err != nil || len(again) != 2 || again[0].ID() != "ovpn-tcp" || again[0].dev != "ovpn-tcp" || again[0].fwChain != "TD-ovpn-tcp" {
		t.Fatalf("load = %v, %v", again, err)
	}

	// Removing keeps the files and stops OpenVPN; the first server stays.
	m, _ := h.get("ovpn-tcp")
	fm := &fakeMgmt{}
	m.b.(*Server).mgmt = fm
	if err := sc.RemoveServer(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.OpenVPNServersDir, "ovpn-tcp")); !os.IsNotExist(err) {
		t.Errorf("directory still there: %v", err)
	}
	if kept, _ := filepath.Glob(filepath.Join(cfg.OpenVPNServersDir, ".removed", "ovpn-tcp-*", ovpnStateFile)); len(kept) != 1 {
		t.Errorf("files not kept: %v", kept)
	}
	if len(fm.sigs) != 1 || fm.sigs[0] != "SIGTERM" {
		t.Errorf("signals = %v", fm.sigs)
	}
	if _, err := sc.ServerInfo(ctx); !IsStatus(err, http.StatusNotFound) {
		t.Errorf("removed server answers: %v", err)
	}
	if err := c.Server(DefaultServerID).RemoveServer(ctx); !IsStatus(err, http.StatusConflict) {
		t.Errorf("remove the first server: %v", err)
	}
	if again, _ := LoadOpenVPN(cfg, log); len(again) != 1 {
		t.Errorf("removed server loaded again: %v", again)
	}
}

func TestAddedOpenVPNSetup(t *testing.T) {
	bin, err := exec.LookPath("openvpn")
	if err != nil {
		t.Skip("openvpn not installed")
	}
	c, h, cfg := ovpnHost(t)
	ctx := context.Background()
	if _, err := c.CreateServer(ctx, api.CreateServer{Type: api.TypeOpenVPN, ID: "ovpn-tcp", Port: 443, Proto: "tcp"}); err != nil {
		t.Fatal(err)
	}
	m, _ := h.get("ovpn-tcp")
	s := m.b.(*Server)
	s.cfg.OpenVPNBin = bin
	s.mgmt = &fakeMgmt{}
	s.restartWait = 100 * time.Millisecond
	sc := c.Server("ovpn-tcp")
	res, err := sc.Setup(ctx, api.SetupRequest{PublicHost: "vpn.example.com", Subnet: "10.9.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cfg.OpenVPNServersDir, "ovpn-tcp")
	for _, want := range []string{"port 443\n", "proto tcp-server\n", "dev ovpn-tcp\ndev-type tun\n", "management 127.0.0.1 2081\n",
		"ca pki/ca.crt\n", "crl-verify crl.pem\n", "client-config-dir ccd\n"} {
		if !strings.Contains(res.ServerConf, want) {
			t.Errorf("server.conf lacks %q:\n%s", want, res.ServerConf)
		}
	}
	for _, f := range []string{"server.conf", "pki/ca.crt", "crl.pem", "ccd", "tls-crypt-v2.key"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	if _, err := sc.IssueCert(ctx, api.IssueCertRequest{Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	prof, err := sc.Profile(ctx, "alice")
	if err != nil || !strings.Contains(string(prof), "remote vpn.example.com 443") || !strings.Contains(string(prof), "proto tcp") {
		t.Errorf("profile: %v\n%s", err, prof)
	}
	// Its own PKI: the first server has none.
	if info, _ := c.Server(DefaultServerID).ServerInfo(ctx); info.PKIReady {
		t.Error("the first server got a PKI")
	}
	// Access rules go to its own chain and interface.
	fw, err := sc.Firewall(ctx)
	if err != nil || !strings.Contains(fw.Rules, "TD-ovpn-tcp") {
		t.Errorf("firewall: %+v, %v", fw, err)
	}
}
