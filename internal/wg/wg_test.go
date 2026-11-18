package wg

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestKeys(t *testing.T) {
	priv, pub, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if !ValidKey(priv) || !ValidKey(pub) || priv == pub {
		t.Fatalf("keys %q %q", priv, pub)
	}
	got, err := PublicKey(priv)
	if err != nil || got != pub {
		t.Fatalf("PublicKey = %q, %v; want %q", got, err, pub)
	}
	// Known pair from wg(8): echo <priv> | wg pubkey
	got, err = PublicKey("yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=")
	if err != nil || got != "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykw=" {
		t.Fatalf("PublicKey(known) = %q, %v", got, err)
	}
	psk, err := GeneratePSK()
	if err != nil || !ValidKey(psk) {
		t.Fatalf("psk %q %v", psk, err)
	}
	for _, bad := range []string{"", "abc", "AAAA", strings.Repeat("A", 43)} {
		if ValidKey(bad) {
			t.Errorf("ValidKey(%q) = true", bad)
		}
	}
}

func TestServerConfig(t *testing.T) {
	got := ServerConfig(Server{
		PrivateKey: "SERVERPRIV",
		Address:    []netip.Prefix{netip.MustParsePrefix("10.9.0.1/24")},
		ListenPort: 51820,
		Extra:      []Setting{{"Jc", "4"}, {"H1", "123"}},
	}, []Peer{
		{Name: "alice\n[Interface]", PublicKey: "ALICE", PresharedKey: "PSK",
			AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.9.0.2/32"), netip.MustParsePrefix("192.168.5.0/24")}},
		{PublicKey: "BOB", AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.9.0.3/32")}},
	})
	want := `# Written by trapdoor; changes made here are overwritten.
[Interface]
PrivateKey = SERVERPRIV
Address = 10.9.0.1/24
ListenPort = 51820
Jc = 4
H1 = 123

# alice [Interface]
[Peer]
PublicKey = ALICE
PresharedKey = PSK
AllowedIPs = 10.9.0.2/32, 192.168.5.0/24

[Peer]
PublicKey = BOB
AllowedIPs = 10.9.0.3/32
`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestClientConfig(t *testing.T) {
	got := ClientConfig(Client{
		PrivateKey: "CPRIV", Address: []netip.Prefix{netip.MustParsePrefix("10.9.0.2/32")},
		DNS: []string{"1.1.1.1", "9.9.9.9"}, MTU: 1380, ServerKey: "SPUB", PresharedKey: "PSK",
		Endpoint: "vpn.example.com:51820", AllowedIPs: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}, Keepalive: 25,
	})
	want := `[Interface]
PrivateKey = CPRIV
Address = 10.9.0.2/32
DNS = 1.1.1.1, 9.9.9.9
MTU = 1380

[Peer]
PublicKey = SPUB
PresharedKey = PSK
Endpoint = vpn.example.com:51820
AllowedIPs = 0.0.0.0/0
PersistentKeepalive = 25
`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestParseDump(t *testing.T) {
	out := "PRIV\tPUB\t51820\toff\n" +
		"ALICE\tPSK\t203.0.113.7:40000\t10.9.0.2/32,192.168.5.0/24\t1795000000\t1000\t2000\t25\n" +
		"BOB\t(none)\t(none)\t10.9.0.3/32\t0\t0\t0\toff\n"
	d, err := ParseDump(out)
	if err != nil {
		t.Fatal(err)
	}
	if d.PublicKey != "PUB" || d.ListenPort != 51820 || len(d.Peers) != 2 {
		t.Fatalf("dump = %+v", d)
	}
	a, b := d.Peers[0], d.Peers[1]
	if a.PublicKey != "ALICE" || a.Endpoint != "203.0.113.7:40000" || len(a.AllowedIPs) != 2 ||
		a.RxBytes != 1000 || a.TxBytes != 2000 || a.LastHandshake.Unix() != 1795000000 {
		t.Errorf("alice = %+v", a)
	}
	if b.Endpoint != "" || !b.LastHandshake.IsZero() || b.Connected(time.Now()) {
		t.Errorf("bob = %+v", b)
	}
	hs := a.LastHandshake
	if !a.Connected(hs.Add(time.Minute)) || a.Connected(hs.Add(ConnectedWithin)) {
		t.Error("Connected window wrong")
	}
	if _, err := ParseDump(""); err == nil {
		t.Error("empty dump accepted")
	}
	if _, err := ParseDump("a\tb\tc\td\nshort\tline"); err == nil {
		t.Error("bad peer line accepted")
	}
}
