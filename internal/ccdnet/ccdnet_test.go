package ccdnet

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
)

// As openvpn-ui wrote it (router with a LAN, default route off, one route, 2FA).
const legacy = `### Automatic generated "nas" settings file - Monday, 01-Jan-26 10:00:00 UTC
ifconfig-push 10.8.0.50 255.255.255.0
iroute 192.168.50.0 255.255.255.0

# Set VPN as default route
# push "redirect-gateway def1"

# Route to office [printer] device internal subnet
push "route 10.20.0.0 255.255.0.0"

#2FA OTP and/or static pass for auth.
#2FA_KEY:00ff
#2FA_USER:nas
#STATIC_PASS:pin
`

const serverConf = `port 1194
server 10.8.0.0 255.255.255.0
topology subnet
client-config-dir ccd
route 192.168.50.0 255.255.255.0
;push "redirect-gateway def1"
`

func mustPrefix(s string) netip.Prefix { return netip.MustParsePrefix(s) }

func TestParseLegacy(t *testing.T) {
	n, err := Parse(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if n.StaticIP != netip.MustParseAddr("10.8.0.50") || n.FullTunnel ||
		len(n.LAN) != 1 || n.LAN[0] != mustPrefix("192.168.50.0/24") ||
		len(n.Routes) != 1 || n.Routes[0] != mustPrefix("10.20.0.0/16") {
		t.Fatalf("net = %+v", n)
	}
}

func TestApplyKeepsOtherLines(t *testing.T) {
	srv := ParseServer(serverConf)
	n := Net{
		StaticIP:   netip.MustParseAddr("10.8.0.60"),
		FullTunnel: true,
		Routes:     []netip.Prefix{mustPrefix("192.168.1.0/24")},
	}
	out := Apply(legacy+"comp-lzo no\n", n, srv)
	want := header + "\n" +
		"ifconfig-push 10.8.0.60 255.255.255.0\n" +
		"push \"route 192.168.1.0 255.255.255.0\"\n" +
		"push \"redirect-gateway def1\"\n"
	if !strings.HasPrefix(out, want) {
		t.Fatalf("got:\n%s\nwant prefix:\n%s", out, want)
	}
	for _, keep := range []string{"#2FA_KEY:00ff", "#STATIC_PASS:pin", "comp-lzo no"} {
		if !strings.Contains(out, keep) {
			t.Errorf("lost %q", keep)
		}
	}
	for _, gone := range []string{"iroute", "10.20.0.0", "Automatic generated", "# Set VPN as default route", "Route to office"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q should be gone:\n%s", gone, out)
		}
	}
	back, _ := Parse(out)
	if back.StaticIP != n.StaticIP || !back.FullTunnel || len(back.Routes) != 1 || len(back.LAN) != 0 {
		t.Errorf("round trip = %+v", back)
	}
	// Clearing everything leaves only the other lines.
	if got := Apply(out, Net{}, srv); strings.Contains(got, header) || !strings.HasPrefix(got, "#2FA OTP") {
		t.Errorf("clear:\n%s", got)
	}
	if got := Apply("", Net{}, srv); got != "" {
		t.Errorf("empty = %q", got)
	}
}

func TestNet30(t *testing.T) {
	srv := ParseServer("server 10.8.0.0 255.255.255.0\n")
	if srv.Topology != TopologyNet30 {
		t.Fatalf("default topology = %s", srv.Topology)
	}
	out := Apply("", Net{StaticIP: netip.MustParseAddr("10.8.0.9")}, srv)
	if !strings.Contains(out, "ifconfig-push 10.8.0.9 10.8.0.10\n") {
		t.Errorf("net30 ifconfig-push:\n%s", out)
	}
	if _, err := Validate("a", Net{StaticIP: netip.MustParseAddr("10.8.0.10")}, srv, nil); !errors.Is(err, ErrInvalid) {
		t.Error("net30 needs the first host of a /30")
	}
}

func TestValidate(t *testing.T) {
	srv := ParseServer(serverConf + "ifconfig-pool 10.8.0.100 10.8.0.200\n")
	used := map[string]netip.Addr{"bob": netip.MustParseAddr("10.8.0.70")}
	ip := func(s string) Net { return Net{StaticIP: netip.MustParseAddr(s)} }

	for _, bad := range []Net{
		ip("10.9.0.5"),   // outside
		ip("10.8.0.0"),   // network
		ip("10.8.0.255"), // broadcast
		ip("10.8.0.1"),   // server
		ip("10.8.0.70"),  // bob's
		{LAN: []netip.Prefix{mustPrefix("10.8.0.0/16")}}, // overlaps the VPN
	} {
		if _, err := Validate("alice", bad, srv, used); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: err = %v", bad, err)
		}
	}
	if w, err := Validate("bob", ip("10.8.0.70"), srv, used); err != nil || len(w) != 0 {
		t.Errorf("bob keeping his own address: %v %v", w, err)
	}
	if w, _ := Validate("alice", ip("10.8.0.150"), srv, used); len(w) != 1 || !strings.Contains(w[0], "dynamic pool") {
		t.Errorf("pool warning = %v", w)
	}
	// LAN without a matching server route → warning with the exact line to add.
	w, err := Validate("alice", Net{LAN: []netip.Prefix{mustPrefix("192.168.60.0/24")}}, srv, used)
	if err != nil || len(w) != 1 || !strings.Contains(w[0], `"route 192.168.60.0 255.255.255.0"`) {
		t.Errorf("missing route warning = %v %v", w, err)
	}
	if w, _ := Validate("alice", Net{LAN: []netip.Prefix{mustPrefix("192.168.50.0/24")}}, srv, used); len(w) != 0 {
		t.Errorf("covered LAN warned: %v", w)
	}
}

func TestParsePrefix(t *testing.T) {
	for in, want := range map[string]string{"192.168.1.7/24": "192.168.1.0/24", "10.0.0.5": "10.0.0.5/32", " 172.16.0.0/12 ": "172.16.0.0/12"} {
		p, err := ParsePrefix(in)
		if err != nil || p.String() != want {
			t.Errorf("ParsePrefix(%q) = %v, %v", in, p, err)
		}
	}
	for _, bad := range []string{"", "x", "10.0.0.0/33"} {
		if _, err := ParsePrefix(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := maskBits("255.0.255.0"); err == nil {
		t.Error("non-contiguous mask accepted")
	}
}
