package agent

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/wg"
)

func peerConf(t *testing.T, c *Client, name string) string {
	t.Helper()
	b, err := c.PeerConf(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWireGuardClientOptions(t *testing.T) {
	c, _, _ := ovpnHost(t)
	ctx := context.Background()
	if _, err := c.CreateServer(ctx, api.CreateServer{Type: api.TypeWireGuard, ID: "wg0", PublicHost: "vpn.example.com",
		Subnet: "10.9.0.0/24", DNS: []string{"10.9.0.1"}}); err != nil {
		t.Fatal(err)
	}
	s := c.Server("wg0")

	// Own settings override the server's; notes are kept.
	ka := 0
	p, err := s.AddPeer(ctx, api.NewPeer{Name: "laptop", Address: "10.9.0.50", PeerSettings: api.PeerSettings{
		Notes: "work laptop", DNS: []string{"1.1.1.1"}, AllowedIPs: []string{"10.9.0.0/24", "192.168.1.5/24"}, MTU: 1380, Keepalive: &ka}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Notes != "work laptop" || p.Address != "10.9.0.50" || !p.HasPSK || p.OwnKey || strings.Join(p.AllowedIPs, ",") != "10.9.0.0/24,192.168.1.0/24" {
		t.Errorf("laptop = %+v", p)
	}
	conf := peerConf(t, s, "laptop")
	for _, want := range []string{"DNS = 1.1.1.1\n", "MTU = 1380\n", "AllowedIPs = 10.9.0.0/24, 192.168.1.0/24\n", "Address = 10.9.0.50/32\n"} {
		if !strings.Contains(conf, want) {
			t.Errorf("laptop config lacks %q:\n%s", want, conf)
		}
	}
	if strings.Contains(conf, "PersistentKeepalive") {
		t.Error("keepalive 0 should turn it off")
	}

	// Own key: no private key here, a placeholder in the config; no PSK.
	_, pub, _ := wg.GenerateKey()
	no := false
	p, err = s.AddPeer(ctx, api.NewPeer{Name: "router", PublicKey: pub, PSK: &no})
	if err != nil || !p.OwnKey || p.PublicKey != pub || p.HasPSK {
		t.Fatalf("router = %+v, %v", p, err)
	}
	if conf := peerConf(t, s, "router"); !strings.Contains(conf, ownKeyPlaceholder) || strings.Contains(conf, "PresharedKey") {
		t.Errorf("router config:\n%s", conf)
	}
	if _, err := s.AddPeer(ctx, api.NewPeer{Name: "router2", PublicKey: pub}); !IsStatus(err, http.StatusConflict) {
		t.Errorf("same public key twice: %v", err)
	}
	for name, bad := range map[string]api.NewPeer{
		"bad key": {Name: "x", PublicKey: "nope"},
		"bad dns": {Name: "x", PeerSettings: api.PeerSettings{DNS: []string{"dns.example.com"}}},
		"bad mtu": {Name: "x", PeerSettings: api.PeerSettings{MTU: 100}},
		"notes":   {Name: "x", PeerSettings: api.PeerSettings{Notes: "a\nb"}},
	} {
		if _, err := s.AddPeer(ctx, bad); !IsStatus(err, http.StatusBadRequest) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// Edit: back to the server's defaults, another address, PSK off.
	p, err = s.UpdatePeer(ctx, "laptop", api.PeerUpdate{Address: "10.9.0.60", PSK: &no, Settings: &api.PeerSettings{Notes: "moved"}})
	if err != nil || p.Address != "10.9.0.60" || p.HasPSK || p.Notes != "moved" || len(p.DNS) != 0 {
		t.Fatalf("update = %+v, %v", p, err)
	}
	conf = peerConf(t, s, "laptop")
	if !strings.Contains(conf, "DNS = 10.9.0.1\n") || !strings.Contains(conf, "PersistentKeepalive = 25\n") || strings.Contains(conf, "PresharedKey") {
		t.Errorf("laptop config with defaults:\n%s", conf)
	}
	if _, err := s.UpdatePeer(ctx, "laptop", api.PeerUpdate{Address: "10.9.0.1"}); !IsStatus(err, http.StatusBadRequest) {
		t.Errorf("the server's address: %v", err)
	}

	// Server settings: client defaults for every config.
	set, err := s.WGSettings(ctx)
	if err != nil || set.Keepalive != 25 || !set.PSK || set.PublicPort != 51820 {
		t.Fatalf("settings = %+v, %v", set, err)
	}
	set.DNS, set.MTU, set.Keepalive, set.PSK, set.PublicHost, set.PublicPort = []string{"9.9.9.9"}, 1300, 15, false, "vpn2.example.com", 443
	if _, err := s.SetWGSettings(ctx, set); err != nil {
		t.Fatal(err)
	}
	conf = peerConf(t, s, "laptop")
	for _, want := range []string{"DNS = 9.9.9.9\n", "MTU = 1300\n", "PersistentKeepalive = 15\n", "Endpoint = vpn2.example.com:443\n"} {
		if !strings.Contains(conf, want) {
			t.Errorf("config after new defaults lacks %q:\n%s", want, conf)
		}
	}
	if p, _ := s.AddPeer(ctx, api.NewPeer{Name: "tablet"}); p.HasPSK {
		t.Error("PSK default off ignored")
	}
	set.AllowedIPs = nil
	if _, err := s.SetWGSettings(ctx, set); !IsStatus(err, http.StatusBadRequest) {
		t.Errorf("no allowed IPs: %v", err)
	}

	// Bulk: names and addresses follow the free ones.
	added, err := s.AddPeers(ctx, api.NewPeers{Prefix: "guest", Count: 3})
	if err != nil || len(added) != 3 || added[0].Name != "guest-1" || added[2].Name != "guest-3" {
		t.Fatalf("bulk = %+v, %v", added, err)
	}
	more, _ := s.AddPeers(ctx, api.NewPeers{Prefix: "guest", Count: 1})
	if len(more) != 1 || more[0].Name != "guest-4" {
		t.Errorf("bulk again = %+v", more)
	}
	if _, err := s.AddPeers(ctx, api.NewPeers{Prefix: "x", Count: 300}); !IsStatus(err, http.StatusBadRequest) {
		t.Errorf("too many: %v", err)
	}
}
