package agent

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/config"
)

func TestStopStartWireGuard(t *testing.T) {
	tools, calls := fakeWG(t)
	dir := t.TempDir()
	old := ipForwardPath
	ipForwardPath = filepath.Join(t.TempDir(), "ip_forward")
	t.Cleanup(func() { ipForwardPath = old })
	cfg := config.Agent{WireGuard: "direct", WireGuardDir: dir, WGToolsDir: tools, IPTablesDir: tools}
	c, _ := wgHost(t, cfg)
	ctx := context.Background()
	if _, err := c.CreateServer(ctx, api.CreateServer{Type: api.TypeWireGuard, ID: "wg0", PublicHost: "vpn.example.com"}); err != nil {
		t.Fatal(err)
	}
	s := c.Server("wg0")
	info, err := s.StopServer(ctx)
	if err != nil || !info.Stopped || info.Running || info.Error != "" || !info.Autostart {
		t.Fatalf("stop = %+v, %v", info, err)
	}
	if !strings.Contains(read(t, calls), "wg-quick down") {
		t.Errorf("not taken down:\n%s", read(t, calls))
	}
	if sess, err := s.Sessions(ctx); err != nil || len(sess.Sessions) != 0 {
		t.Errorf("sessions while stopped = %+v, %v", sess, err)
	}
	// Changes while stopped are saved, not applied.
	before := strings.Count(read(t, calls), "wg-quick up")
	if _, err := s.AddPeer(ctx, api.NewPeer{Name: "phone"}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(read(t, calls), "wg-quick up") != before || strings.Contains(read(t, calls), "syncconf") {
		t.Error("applied while stopped")
	}
	if info, err = s.StartServer(ctx); err != nil || info.Stopped {
		t.Fatalf("start = %+v, %v", info, err)
	}
	if strings.Count(read(t, calls), "wg-quick up") != before+1 {
		t.Errorf("not brought up:\n%s", read(t, calls))
	}

	// Autostart off: the agent starting again leaves it stopped.
	if info, err = s.SetAutostart(ctx, false); err != nil || info.Autostart {
		t.Fatalf("autostart = %+v, %v", info, err)
	}
	c2, _ := wgHost(t, cfg)
	if info, _ := c2.Server("wg0").ServerInfo(ctx); !info.Stopped || info.Autostart {
		t.Errorf("after an agent restart = %+v", info)
	}
}

func TestStopStartOpenVPN(t *testing.T) {
	c, h, cfg := ovpnHost(t)
	ctx := context.Background()
	m, _ := h.get(DefaultServerID)
	srv := m.b.(*Server)
	fm := &fakeMgmt{}
	srv.mgmt = fm
	srv.restartWait = 50e6
	s := c.Server(DefaultServerID)
	info, err := s.StopServer(ctx)
	if err != nil || !info.Stopped || info.MgmtError != "" {
		t.Fatalf("stop = %+v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(cfg.OpenVPNDir, "trapdoor.stopped")); err != nil {
		t.Error("no stopped marker for the container")
	}
	if len(fm.sigs) != 1 || fm.sigs[0] != "SIGTERM" {
		t.Errorf("signals = %v", fm.sigs)
	}
	if sess, err := s.Sessions(ctx); err != nil || len(sess.Sessions) != 0 {
		t.Errorf("sessions while stopped = %+v, %v", sess, err)
	}
	if _, err := s.RestartServer(ctx, api.RestartRequest{}); !IsStatus(err, http.StatusConflict) {
		t.Errorf("restart while stopped: %v", err)
	}
	if info, err = s.StartServer(ctx); err != nil || info.Stopped {
		t.Fatalf("start = %+v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(cfg.OpenVPNDir, "trapdoor.stopped")); err == nil {
		t.Error("stopped marker left")
	}
	if info, err = s.SetAutostart(ctx, false); err != nil || info.Autostart {
		t.Fatalf("autostart off = %+v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(cfg.OpenVPNDir, "trapdoor.noautostart")); err != nil {
		t.Error("no noautostart marker for the container")
	}
	if info, _ = s.SetAutostart(ctx, true); !info.Autostart {
		t.Error("autostart not back on")
	}
}
