package agent

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/config"
	"github.com/shuricksumy/trapdoor/internal/mgmt"
	"github.com/shuricksumy/trapdoor/internal/shaping"
)

// sessionsMgmt answers status with the given sessions.
type sessionsMgmt struct {
	*fakeMgmt
	sessions []mgmt.Session
}

func (m sessionsMgmt) Status(context.Context) (mgmt.Status, error) {
	return mgmt.Status{Sessions: m.sessions}, nil
}

// fakeTC puts tc and ip first on PATH; they log their arguments.
func fakeTC(t *testing.T, fail bool) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	rc := "0"
	if fail {
		rc = "1"
	}
	for _, name := range []string{"tc", "ip"} {
		body := "#!/bin/sh\necho \"" + name + " $*\" >>" + log + "\ncase \"$*\" in *' add '*|*' add') [ " + rc + " = 0 ] || { echo 'RTNETLINK answers: No such file or directory' >&2; exit 2; } ;; esac\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func shapingServer(t *testing.T, mode string) (*Client, string) {
	t.Helper()
	return firewallClient(t, config.Agent{Firewall: mode})
}

func ovpnTestServer(t *testing.T, cfg config.Agent) (*Server, *Client) {
	t.Helper()
	s := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	s.mgmt = &fakeMgmt{}
	s.restartWait = 2 * time.Second
	srv := httptest.NewServer(serve(s))
	t.Cleanup(srv.Close)
	return s, NewClient(srv.URL, "secret", 10*time.Second).Server(DefaultServerID)
}

func TestShapingDirect(t *testing.T) {
	calls := fakeTC(t, false)
	dir := t.TempDir()
	cfg := config.Agent{Token: "secret", OpenVPNDir: dir, CCDDir: filepath.Join(dir, "ccd"), Firewall: "direct"}
	_ = os.Mkdir(cfg.CCDDir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "server.conf"), []byte("server 10.8.0.0 255.255.255.0\ntopology subnet\n"), 0o644)
	_ = os.WriteFile(filepath.Join(cfg.CCDDir, "alice"), []byte("ifconfig-push 10.8.0.10 255.255.255.0\n"), 0o644)
	s, c := ovpnTestServer(t, cfg)
	s.mgmt = sessionsMgmt{&fakeMgmt{}, []mgmt.Session{{Name: "alice", VirtualAddress: "10.8.0.99"}, {Name: "bob", VirtualAddress: "10.8.0.7"}}}
	idx := 7
	s.shaper().links = func() ([]net.Interface, error) {
		return []net.Interface{{Name: "lo", Index: 1}, {Name: "tun5", Index: idx}}, nil
	}
	ctx := context.Background()

	st, err := c.Shaping(ctx)
	if err != nil || !st.Applied || st.Mode != "direct" || st.Script != "" {
		t.Fatalf("initial = %+v %v", st, err)
	}
	// nothing asked: nothing run
	if st, err := c.SetShaping(ctx, api.Shaping{}); err != nil || !st.Applied {
		t.Fatalf("empty = %+v %v", st, err)
	}
	if _, err := os.Stat(calls); err == nil {
		t.Fatal("tc ran without limits")
	}
	if _, err := c.SetShaping(ctx, api.Shaping{Default: api.Speed{Block: true}}); err == nil {
		t.Error("blocking everyone accepted")
	}
	if _, err := c.SetShaping(ctx, api.Shaping{Clients: map[string]api.Speed{"a/b": {Down: 1}}}); err == nil {
		t.Error("bad name accepted")
	}

	st, err = c.SetShaping(ctx, api.Shaping{Default: api.Speed{Down: 50000}, Clients: map[string]api.Speed{"bob": {Down: 1000, Up: 500}, "carol": {Block: true}}})
	if err != nil || !st.Applied || st.Error != "" {
		t.Fatalf("set = %+v %v", st, err)
	}
	// alice: her fixed address, not the session's; bob: his session's; carol: not connected
	if st.Addresses["alice"] != "10.8.0.10" || st.Addresses["bob"] != "10.8.0.7" || st.Addresses["carol"] != "" {
		t.Errorf("addresses %v", st.Addresses)
	}
	log, _ := os.ReadFile(calls)
	for _, want := range []string{"tc qdisc add dev tun5 root handle 1: htb default 0", "match ip dst 10.8.0.10/32", "rate 50000kbit", "ip link add tq-openvpn type ifb", "match ip src 10.8.0.7/32"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("missing %q in\n%s", want, log)
		}
	}
	// the same again: not run again
	_ = os.Remove(calls)
	if st, _ := c.SetShaping(ctx, st.Shaping); !st.Applied {
		t.Errorf("again = %+v", st)
	}
	if _, err := os.Stat(calls); err == nil {
		t.Error("ran again without a change")
	}
	// the device was recreated (OpenVPN restarted): applied again
	idx = 8
	s.shaper().refresh(ctx)
	if log, _ := os.ReadFile(calls); !strings.Contains(string(log), "tc qdisc add dev tun5 root") {
		t.Errorf("not re-applied on a new device:\n%s", log)
	}
	// no device (stopped): waiting
	s.shaper().links = func() ([]net.Interface, error) { return nil, nil }
	if st, _ := c.Shaping(ctx); st.Applied || !st.Waiting {
		t.Errorf("no device = %+v", st)
	}
}

func TestShapingDirectError(t *testing.T) {
	fakeTC(t, true)
	dir := t.TempDir()
	cfg := config.Agent{Token: "secret", OpenVPNDir: dir, CCDDir: filepath.Join(dir, "ccd"), Firewall: "direct"}
	_ = os.Mkdir(cfg.CCDDir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "server.conf"), []byte("server 10.8.0.0 255.255.255.0\ntopology subnet\n"), 0o644)
	_ = os.WriteFile(filepath.Join(cfg.CCDDir, "alice"), []byte("ifconfig-push 10.8.0.10 255.255.255.0\n"), 0o644)
	s, c := ovpnTestServer(t, cfg)
	s.shaper().links = func() ([]net.Interface, error) { return []net.Interface{{Name: "tun0", Index: 3}}, nil }
	st, err := c.SetShaping(context.Background(), api.Shaping{Clients: map[string]api.Speed{"alice": {Down: 1000}}})
	if err != nil || st.Applied || !strings.Contains(st.Error, "No such file") {
		t.Fatalf("set = %+v %v", st, err)
	}
}

func TestShapingFile(t *testing.T) {
	c, dir := shapingServer(t, "file")
	ctx := context.Background()
	// Play the VPN container: answer for whatever script appears.
	answer := make(chan string, 1)
	answer <- "ok"
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		state := "ok"
		for {
			select {
			case <-stop:
				return
			case s := <-answer:
				state = s
			case <-time.After(50 * time.Millisecond):
			}
			if data, err := os.ReadFile(filepath.Join(dir, "trapdoor-shaping.sh")); err == nil {
				status := filepath.Join(dir, "trapdoor-shaping.status")
				_ = os.WriteFile(status+".tmp", []byte(state+" "+shaping.Hash(string(data))+" because\n"), 0o644)
				_ = os.Rename(status+".tmp", status)
			}
		}
	}()
	st, err := c.SetShaping(ctx, api.Shaping{Clients: map[string]api.Speed{"alice": {Down: 2000}}})
	if err != nil || !st.Applied || st.Mode != "file" || !strings.Contains(st.Script, "match ip dst 10.8.0.10/32") {
		t.Fatalf("set = %+v %v", st, err)
	}
	if !strings.Contains(st.Script, "# IFACE=tun+") {
		t.Errorf("script:\n%s", st.Script)
	}
	answer <- "waiting"
	time.Sleep(200 * time.Millisecond)
	if st, _ := c.Shaping(ctx); st.Applied || !st.Waiting {
		t.Errorf("waiting = %+v", st)
	}
	answer <- "error"
	time.Sleep(200 * time.Millisecond)
	if st, _ := c.Shaping(ctx); st.Applied || !strings.Contains(st.Error, "could not apply the speed limits: because") {
		t.Errorf("error = %+v", st)
	}
}

func TestShapingOff(t *testing.T) {
	c, _ := shapingServer(t, "off")
	if _, err := c.SetShaping(context.Background(), api.Shaping{Clients: map[string]api.Speed{"alice": {Down: 1}}}); err == nil || !strings.Contains(err.Error(), "TD_FIREWALL=off") {
		t.Errorf("off: %v", err)
	}
}

// A WireGuard server: every client has an address; the script goes to
// <dir>/<id>.shaping.sh for the VPN container. Removing the server takes
// the files away.
func TestShapingWireGuard(t *testing.T) {
	old := shapingWait
	shapingWait = 300 * time.Millisecond
	t.Cleanup(func() { shapingWait = old })
	dir := t.TempDir()
	c, _ := wgHost(t, config.Agent{WireGuard: "file", WireGuardDir: dir, Firewall: "file"})
	ctx := context.Background()
	if _, err := c.CreateServer(ctx, api.CreateServer{Type: api.TypeWireGuard, ID: "wg0", PublicHost: "203.0.113.5"}); err != nil {
		t.Fatal(err)
	}
	w := c.Server("wg0")
	p, err := w.AddPeer(ctx, api.NewPeer{Name: "anna"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := w.SetShaping(ctx, api.Shaping{Default: api.Speed{Down: 8000, Up: 2000}})
	if err != nil || st.Addresses["anna"] != p.Address {
		t.Fatalf("set = %+v %v", st, err)
	}
	script := read(t, filepath.Join(dir, "wg0.shaping.sh"))
	for _, want := range []string{"# IFACE=wg0", "IFB=tq-wg0", "match ip dst " + p.Address + "/32", "rate 8000kbit", "rate 2000kbit"} {
		if !strings.Contains(script, want) {
			t.Errorf("missing %q in\n%s", want, script)
		}
	}
	if st.Applied || !strings.Contains(st.Error, "not applied the current speed limits") {
		t.Errorf("no container answer yet = %+v", st)
	}
	if err := w.RemoveServer(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "wg0.shaping.sh")); err == nil {
		t.Error("script left after the server was removed")
	}
}
