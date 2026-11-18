package agent

import (
	"context"
	"fmt"
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
	"github.com/shuricksumy/trapdoor/internal/wg"
)

// fakeWG writes wg and wg-quick scripts that log their calls. An interface
// is "up" while <tools>/up-<id> exists; `wg show <id> dump` prints
// <tools>/dump-<id>.
func fakeWG(t *testing.T) (tools, calls string) {
	t.Helper()
	tools = t.TempDir()
	calls = filepath.Join(tools, "calls")
	wgq := `#!/bin/sh
echo wg-quick "$@" >>` + calls + `
id=$(basename "$2" .conf)
case $1 in
up) touch ` + tools + `/up-$id ;;
down) rm -f ` + tools + `/up-$id ;;
strip) grep -v -e '^Address' -e '^PostUp' -e '^PostDown' -e '^MTU' "$2" ;;
esac
`
	wgs := `#!/bin/sh
echo wg "$@" >>` + calls + `
case $1 in
show) [ -f ` + tools + `/up-$2 ] || { echo "Unable to access interface: No such device" >&2; exit 1; }
	[ "$3" = dump ] && cat ` + tools + `/dump-$2 2>/dev/null; exit 0 ;;
syncconf) cp "$3" ` + tools + `/synced-$2 ;;
esac
`
	// -C fails until the rule was inserted, like the real iptables.
	ipt := `#!/bin/sh
echo iptables "$@" >>` + calls + `
if [ "$1" = -C ] || [ "$3" = -C ]; then grep -qF -- "$(echo "$@" | sed 's/-C/-I/')" ` + calls + `; exit $?; fi
`
	for name, body := range map[string]string{"wg-quick": wgq, "wg": wgs, "iptables": ipt} {
		if err := os.WriteFile(filepath.Join(tools, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return tools, calls
}

func wgHost(t *testing.T, cfg config.Agent) (*Client, *Host) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewHost("secret", log)
	wgs, err := LoadWireGuard(context.Background(), cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	h.EnableWireGuard(cfg, wgs)
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "secret", 10*time.Second), h
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWireGuardServer(t *testing.T) {
	tools, calls := fakeWG(t)
	dir := t.TempDir()
	fwd := filepath.Join(t.TempDir(), "ip_forward")
	old := ipForwardPath
	ipForwardPath = fwd
	t.Cleanup(func() { ipForwardPath = old })
	cfg := config.Agent{WireGuard: "direct", WireGuardDir: dir, WGToolsDir: tools, IPTablesDir: tools}
	c, _ := wgHost(t, cfg)
	ctx := context.Background()

	info, err := c.CreateServer(ctx, api.CreateServer{Type: api.TypeWireGuard, ID: "wg0", Name: "Home",
		PublicHost: "vpn.example.com", DNS: []string{"10.9.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	if info.Type != api.TypeWireGuard || info.Subnet != "10.9.0.0/24" || info.Endpoint != "vpn.example.com:51820" ||
		!wg.ValidKey(info.PublicKey) || !info.Running {
		t.Fatalf("info = %+v", info)
	}
	if !strings.Contains(read(t, calls), "wg-quick up "+filepath.Join(dir, "wg0.conf")) {
		t.Errorf("interface not brought up: %s", read(t, calls))
	}
	conf := read(t, filepath.Join(dir, "wg0.conf"))
	if !strings.Contains(conf, "Address = 10.9.0.1/24") || !strings.Contains(conf, "ListenPort = 51820") ||
		strings.Contains(conf, "PostUp") {
		t.Errorf("server conf:\n%s", conf)
	}
	log := read(t, calls)
	for _, want := range []string{"iptables -I FORWARD -i wg0 -j ACCEPT", "iptables -I FORWARD -o wg0 -j ACCEPT",
		"iptables -t nat -I POSTROUTING -s 10.9.0.0/24 ! -o wg0 -j MASQUERADE"} {
		if strings.Count(log, want) != 1 {
			t.Errorf("want %q once in:\n%s", want, log)
		}
	}
	if read(t, fwd) != "1\n" {
		t.Error("IP forwarding not turned on")
	}
	if fi, _ := os.Stat(filepath.Join(dir, "wg0.json")); fi.Mode().Perm() != 0o600 {
		t.Errorf("state file mode %v", fi.Mode())
	}

	for _, tc := range []struct {
		req  api.CreateServer
		code int
	}{
		{api.CreateServer{Type: api.TypeWireGuard, ID: "wg0", PublicHost: "h", Port: 51821, Subnet: "10.10.0.0/24"}, 409},
		{api.CreateServer{Type: api.TypeWireGuard, ID: "wg1", PublicHost: "h", Subnet: "10.10.0.0/24"}, 409}, // port
		{api.CreateServer{Type: api.TypeWireGuard, ID: "wg1", PublicHost: "h", Port: 51821, Subnet: "10.9.0.128/25"}, 409},
		{api.CreateServer{Type: api.TypeWireGuard, ID: "Wg-1", PublicHost: "h", Port: 51821}, 400},
		{api.CreateServer{Type: api.TypeWireGuard, ID: "averyveryverylongname", PublicHost: "h", Port: 51821}, 400},
		{api.CreateServer{Type: api.TypeWireGuard, ID: "wg1", Port: 51821, Subnet: "10.10.0.0/24"}, 400}, // no host
		{api.CreateServer{Type: api.TypeWireGuard, ID: "wg1", PublicHost: "h", Port: 51821, Subnet: "fd00::/64"}, 400},
		{api.CreateServer{Type: api.TypeOpenVPN, ID: "ovpn2", PublicHost: "h"}, 400},
	} {
		if _, err := c.CreateServer(ctx, tc.req); !IsStatus(err, tc.code) {
			t.Errorf("create %+v: err = %v, want %d", tc.req, err, tc.code)
		}
	}

	s := c.Server("wg0")
	alice, err := s.AddPeer(ctx, api.NewPeer{Name: "alice"})
	if err != nil || alice.Address != "10.9.0.2" || !alice.HasPSK || !wg.ValidKey(alice.PublicKey) {
		t.Fatalf("alice = %+v, %v", alice, err)
	}
	bob, err := s.AddPeer(ctx, api.NewPeer{Name: "bob", NoPSK: true, LAN: []string{"192.168.5.1/24"}})
	if err != nil || bob.Address != "10.9.0.3" || bob.HasPSK || fmt.Sprint(bob.LAN) != "[192.168.5.0/24]" {
		t.Fatalf("bob = %+v, %v", bob, err)
	}
	for _, bad := range []api.NewPeer{{Name: "alice"}, {Name: "a b"}, {Name: "x", Address: "10.9.0.2"},
		{Name: "x", Address: "10.9.0.1"}, {Name: "x", Address: "10.8.0.5"}, {Name: "x", Address: "10.9.0.255"}, {Name: "x", LAN: []string{"nope"}}} {
		if _, err := s.AddPeer(ctx, bad); err == nil {
			t.Errorf("AddPeer(%+v) accepted", bad)
		}
	}
	if got := read(t, filepath.Join(tools, "synced-wg0")); !strings.Contains(got, alice.PublicKey) ||
		!strings.Contains(got, "AllowedIPs = 10.9.0.3/32, 192.168.5.0/24") || strings.Contains(got, "Address") {
		t.Errorf("synced config:\n%s", got)
	}

	cc, err := s.PeerConf(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Address = 10.9.0.2/32", "DNS = 10.9.0.1", "PublicKey = " + info.PublicKey,
		"PresharedKey = ", "Endpoint = vpn.example.com:51820", "AllowedIPs = 0.0.0.0/0, ::/0", "PersistentKeepalive = 25"} {
		if !strings.Contains(string(cc), want) {
			t.Errorf("client conf lacks %q:\n%s", want, cc)
		}
	}

	off := true
	if p, err := s.UpdatePeer(ctx, "bob", api.PeerUpdate{Disabled: &off}); err != nil || !p.Disabled {
		t.Fatalf("disable bob = %+v, %v", p, err)
	}
	if strings.Contains(read(t, filepath.Join(dir, "wg0.conf")), bob.PublicKey) {
		t.Error("disabled bob still in the server config")
	}
	if _, err := s.PeerConf(ctx, "bob"); !IsStatus(err, http.StatusConflict) {
		t.Errorf("conf of disabled peer: %v", err)
	}
	if _, err := s.Peer(ctx, "carol"); !IsStatus(err, http.StatusNotFound) {
		t.Errorf("unknown peer: %v", err)
	}
	if err := s.KillSession(ctx, 1); !IsStatus(err, http.StatusConflict) {
		t.Errorf("kill: %v", err)
	}

	// Live state: counters run for the interface's lifetime, a session
	// counts from its start.
	dump := func(hs time.Time, rx, tx int64) {
		ts := int64(0)
		if !hs.IsZero() {
			ts = hs.Unix()
		}
		body := fmt.Sprintf("PRIV\t%s\t51820\toff\n%s\tPSK\t203.0.113.7:4000\t10.9.0.2/32\t%d\t%d\t%d\t25\n", info.PublicKey, alice.PublicKey, ts, rx, tx)
		if err := os.WriteFile(filepath.Join(tools, "dump-wg0"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// alice was added by this agent: her first session counts from zero.
	hs := time.Now().Add(-5 * time.Second)
	dump(hs, 300, 400)
	if ss, err := s.Sessions(ctx); err != nil || len(ss.Sessions) != 1 || ss.Sessions[0].BytesReceived != 300 {
		t.Fatalf("first session = %+v, %v", ss, err)
	}
	dump(time.Now().Add(-10*time.Minute), 500, 600) // she went away
	if ss, err := s.Sessions(ctx); err != nil || len(ss.Sessions) != 0 || ss.BytesIn != 500 {
		t.Fatalf("sessions after = %+v, %v", ss, err)
	}
	hs = time.Now().Add(-5 * time.Second)
	dump(hs, 1500, 2600)
	ss, err := s.Sessions(ctx)
	if err != nil || len(ss.Sessions) != 1 {
		t.Fatalf("sessions = %+v, %v", ss, err)
	}
	if a := ss.Sessions[0]; a.Name != "alice" || a.BytesReceived != 1000 || a.BytesSent != 2000 ||
		a.RealAddress != "203.0.113.7:4000" || a.VirtualAddress != "10.9.0.2" || a.ConnectedSince.Unix() != hs.Unix() || a.ClientID <= 0 {
		t.Errorf("alice session = %+v", a)
	}
	dump(time.Now(), 2000, 2600) // handshake renewed: same session
	if ss, _ := s.Sessions(ctx); len(ss.Sessions) != 1 || ss.Sessions[0].BytesReceived != 1500 || ss.Sessions[0].ConnectedSince.Unix() != hs.Unix() {
		t.Errorf("session after renewal = %+v", ss.Sessions)
	}
	peers, err := s.Peers(ctx)
	if err != nil || len(peers) != 2 || !peers[0].Connected || peers[0].BytesReceived != 2000 || peers[1].Connected {
		t.Errorf("peers = %+v, %v", peers, err)
	}
	dump(time.Now().Add(-time.Hour), 2000, 2600)
	if ss, _ := s.Sessions(ctx); len(ss.Sessions) != 0 {
		t.Errorf("stale handshake still a session: %+v", ss.Sessions)
	}

	// A restarted agent reads the same servers back.
	c2, _ := wgHost(t, cfg)
	all, err := c2.Info(ctx)
	if err != nil || len(all.Servers) != 1 || all.Servers[0].PublicKey != info.PublicKey || all.Servers[0].Name != "Home" {
		t.Fatalf("reloaded = %+v, %v", all, err)
	}
	if ps, _ := c2.Server("wg0").Peers(ctx); len(ps) != 2 || !ps[1].Disabled {
		t.Errorf("reloaded peers = %+v", ps)
	}

	if err := s.RemovePeer(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read(t, filepath.Join(dir, "wg0.conf")), alice.PublicKey) {
		t.Error("removed alice still in the config")
	}
	if err := s.RemoveServer(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "wg0.json")); !os.IsNotExist(err) {
		t.Errorf("state left behind: %v", err)
	}
	if log := read(t, calls); !strings.Contains(log, "wg-quick down") || !strings.Contains(log, "iptables -t nat -D POSTROUTING -s 10.9.0.0/24 ! -o wg0 -j MASQUERADE") {
		t.Errorf("interface not taken down or NAT left:\n%s", log)
	}
	if all, _ := c.Info(ctx); len(all.Servers) != 0 {
		t.Errorf("servers after remove = %+v", all.Servers)
	}
	if err := c.Server("wg0").RemoveServer(ctx); !IsStatus(err, http.StatusNotFound) {
		t.Errorf("remove again: %v", err)
	}
}

func TestWireGuardFileMode(t *testing.T) {
	dir := t.TempDir()
	c, _ := wgHost(t, config.Agent{WireGuard: "file", WireGuardDir: dir})
	ctx := context.Background()
	info, err := c.CreateServer(ctx, api.CreateServer{Type: api.TypeWireGuard, ID: "wg0", PublicHost: "203.0.113.5"})
	if err != nil {
		t.Fatal(err)
	}
	if info.Running || !strings.Contains(info.Error, "VPN container") {
		t.Errorf("info without a dump = %+v", info)
	}
	if strings.Contains(read(t, filepath.Join(dir, "wg0.conf")), "PostUp") {
		t.Error("file mode config has wg-quick NAT hooks; the container does NAT")
	}
	if err := os.WriteFile(filepath.Join(dir, "wg0.dump"), []byte("PRIV\t"+info.PublicKey+"\t51820\toff\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if info, _ := c.Server("wg0").ServerInfo(ctx); !info.Running || info.Error != "" {
		t.Errorf("info with a dump = %+v", info)
	}
	old := time.Now().Add(-2 * wgDumpMaxAge)
	_ = os.Chtimes(filepath.Join(dir, "wg0.dump"), old, old)
	if _, err := c.Server("wg0").Sessions(ctx); !IsStatus(err, http.StatusBadGateway) {
		t.Errorf("stale dump: %v", err)
	}
	_ = os.WriteFile(filepath.Join(dir, "wg0.error"), []byte("wg-quick: address already in use\n"), 0o644)
	if info, _ := c.Server("wg0").ServerInfo(ctx); !strings.Contains(info.Error, "address already in use") {
		t.Errorf("container error not reported: %+v", info)
	}
}

func TestWireGuardOff(t *testing.T) {
	c, _ := wgHost(t, config.Agent{WireGuard: "off", WireGuardDir: t.TempDir()})
	if _, err := c.CreateServer(context.Background(), api.CreateServer{Type: api.TypeWireGuard, ID: "wg0", PublicHost: "h"}); !IsStatus(err, http.StatusBadRequest) {
		t.Errorf("create with WireGuard off: %v", err)
	}
}
