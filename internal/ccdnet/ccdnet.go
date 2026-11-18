// Package ccdnet reads and writes the network settings of a client's ccd
// file (static VPN address, routes, full tunnel) and the matching parts of
// server.conf. Like openvpn-ui, it writes plain OpenVPN directives:
//
//	ifconfig-push 10.8.0.50 255.255.255.0   static VPN address (topology subnet)
//	iroute 192.168.50.0 255.255.255.0       a LAN behind this client
//	push "route 192.168.1.0 255.255.255.0"  a network this client can reach
//	push "redirect-gateway def1"            all traffic through the VPN
//
// Every other line (2FA settings, custom directives, comments) is kept.
package ccdnet

import (
	"bufio"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// Net is a client's network settings. Prefixes are IPv4, masked.
type Net struct {
	StaticIP   netip.Addr     // invalid = dynamic
	FullTunnel bool           // push "redirect-gateway def1"
	Routes     []netip.Prefix // pushed to the client
	LAN        []netip.Prefix // iroute: networks behind the client
}

const header = "# tunnelward: network settings"

// Parse reads the network lines of a ccd file.
func Parse(ccd string) (Net, error) {
	var n Net
	sc := bufio.NewScanner(strings.NewReader(ccd))
	for sc.Scan() {
		f, ok := directive(sc.Text())
		if !ok {
			continue
		}
		switch {
		case f[0] == "ifconfig-push" && len(f) >= 2:
			a, err := netip.ParseAddr(f[1])
			if err != nil || !a.Is4() {
				return n, fmt.Errorf("ifconfig-push: bad address %q", f[1])
			}
			n.StaticIP = a
		case f[0] == "iroute" && len(f) >= 3:
			p, err := prefix(f[1], f[2])
			if err != nil {
				return n, fmt.Errorf("iroute: %w", err)
			}
			n.LAN = append(n.LAN, p)
		case f[0] == "push" && len(f) >= 2:
			inner := strings.Fields(f[1])
			switch {
			case len(inner) >= 3 && inner[0] == "route":
				p, err := prefix(inner[1], inner[2])
				if err != nil {
					return n, fmt.Errorf("push route: %w", err)
				}
				n.Routes = append(n.Routes, p)
			case len(inner) >= 1 && inner[0] == "redirect-gateway":
				n.FullTunnel = true
			}
		}
	}
	return n, sc.Err()
}

// directive splits an active config line; push "..." keeps its quoted
// argument as one field. Comments and blank lines return ok=false.
func directive(line string) ([]string, bool) {
	t := strings.TrimSpace(line)
	if t == "" || t[0] == '#' || t[0] == ';' {
		return nil, false
	}
	name, rest, _ := strings.Cut(t, " ")
	rest = strings.TrimSpace(rest)
	if name == "push" {
		return []string{name, strings.Trim(rest, `"`)}, true
	}
	return append([]string{name}, strings.Fields(rest)...), true
}

// managed reports whether a line is one Apply rewrites.
func managed(line string) bool {
	t := strings.TrimSpace(line)
	switch {
	case t == header,
		strings.HasPrefix(t, "### Automatic generated"), // openvpn-ui header
		t == "# Set VPN as default route",               // openvpn-ui comment
		t == `# push "redirect-gateway def1"`,           // openvpn-ui disabled default route
		strings.HasPrefix(t, "# Route to ") && strings.HasSuffix(t, "device internal subnet"):
		return true
	}
	f, ok := directive(line)
	if !ok {
		return false
	}
	switch f[0] {
	case "ifconfig-push", "iroute":
		return true
	case "push":
		inner := strings.Fields(f[1])
		return len(inner) > 0 && (inner[0] == "route" || inner[0] == "redirect-gateway")
	}
	return false
}

// Topology of the VPN subnet, from server.conf.
type Topology string

const (
	TopologySubnet Topology = "subnet"
	TopologyNet30  Topology = "net30"
	TopologyP2P    Topology = "p2p"
)

// Apply returns ccd with its network lines replaced by n.
func Apply(ccd string, n Net, srv Server) string {
	var kept []string
	for _, line := range strings.Split(strings.TrimRight(ccd, "\n"), "\n") {
		if !managed(line) {
			kept = append(kept, line)
		}
	}
	// Drop blank lines left at the top by removed lines.
	for len(kept) > 0 && strings.TrimSpace(kept[0]) == "" {
		kept = kept[1:]
	}

	var b strings.Builder
	block := n.block(srv)
	if block != "" {
		b.WriteString(header + "\n" + block)
	}
	rest := strings.TrimRight(strings.Join(kept, "\n"), "\n")
	if rest != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(rest + "\n")
	}
	return b.String()
}

func (n Net) block(srv Server) string {
	var b strings.Builder
	if n.StaticIP.IsValid() {
		second := maskString(srv.Subnet.Bits())
		if srv.Topology == TopologyNet30 || srv.Topology == TopologyP2P {
			second = n.StaticIP.Next().String() // the peer address of the /30 pair
		}
		fmt.Fprintf(&b, "ifconfig-push %s %s\n", n.StaticIP, second)
	}
	for _, p := range n.LAN {
		fmt.Fprintf(&b, "iroute %s %s\n", p.Addr(), maskString(p.Bits()))
	}
	for _, p := range n.Routes {
		fmt.Fprintf(&b, "push \"route %s %s\"\n", p.Addr(), maskString(p.Bits()))
	}
	if n.FullTunnel {
		b.WriteString("push \"redirect-gateway def1\"\n")
	}
	return b.String()
}

// ErrInvalid marks settings that cannot be written.
var ErrInvalid = errors.New("invalid network settings")

// Validate checks n against the server and the static IPs in use by other
// clients (name → address). It returns warnings for things that are allowed
// but probably not what the admin wants.
func Validate(name string, n Net, srv Server, used map[string]netip.Addr) (warnings []string, err error) {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, a...)...)
	}

	if n.StaticIP.IsValid() {
		ip := n.StaticIP
		if !srv.Subnet.IsValid() {
			return nil, bad("server.conf has no \"server\" line, so a static address cannot be checked")
		}
		if !ip.Is4() || !srv.Subnet.Contains(ip) {
			return nil, bad("%s is not in the VPN subnet %s", ip, srv.Subnet)
		}
		if ip == srv.Subnet.Addr() || ip == lastAddr(srv.Subnet) {
			return nil, bad("%s is the network or broadcast address of %s", ip, srv.Subnet)
		}
		if ip == srv.ServerIP() {
			return nil, bad("%s is the server's own address", ip)
		}
		if srv.Topology == TopologyNet30 && (ip.As4()[3]%4 != 1) {
			return nil, bad("with topology net30 the address must be the first host of a /30 (…1, …5, …9, …)")
		}
		for other, a := range used {
			if other != name && a == ip {
				return nil, bad("%s is already used by %s", ip, other)
			}
		}
		if srv.InPool(ip) {
			warnings = append(warnings, fmt.Sprintf("%s is inside the dynamic pool %s-%s; another client may get it while %s is offline", ip, srv.PoolStart, srv.PoolEnd, name))
		}
	}
	for _, p := range append(slices.Clone(n.Routes), n.LAN...) {
		if !p.Addr().Is4() {
			return nil, bad("%s: only IPv4 networks are supported", p)
		}
	}
	for _, p := range n.LAN {
		if srv.Subnet.IsValid() && p.Overlaps(srv.Subnet) {
			return nil, bad("%s overlaps the VPN subnet %s", p, srv.Subnet)
		}
		if !srv.HasRoute(p) {
			warnings = append(warnings, fmt.Sprintf("server.conf needs \"route %s %s\" so the server sends traffic for %s to the VPN", p.Addr(), maskString(p.Bits()), p))
		}
	}
	if len(n.LAN) > 0 && !srv.ClientConfigDir {
		warnings = append(warnings, "server.conf has no client-config-dir, so these settings are not used")
	}
	if n.FullTunnel && srv.PushesFullTunnel {
		warnings = append(warnings, "server.conf already sends all traffic of every client through the VPN")
	}
	return warnings, nil
}

// ParsePrefix accepts "192.168.1.0/24" or a bare address (/32).
func ParsePrefix(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "/") {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("%w: %q is not a network", ErrInvalid, s)
		}
		return netip.PrefixFrom(a, a.BitLen()), nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%w: %q is not a network", ErrInvalid, s)
	}
	return p.Masked(), nil
}

func prefix(addr, mask string) (netip.Prefix, error) {
	a, err := netip.ParseAddr(addr)
	if err != nil || !a.Is4() {
		return netip.Prefix{}, fmt.Errorf("bad address %q", addr)
	}
	bits, err := maskBits(mask)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a, bits).Masked(), nil
}

func maskBits(mask string) (int, error) {
	m, err := netip.ParseAddr(mask)
	if err != nil || !m.Is4() {
		return 0, fmt.Errorf("bad netmask %q", mask)
	}
	b := m.As4()
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	bits := 0
	for v&0x80000000 != 0 {
		bits++
		v <<= 1
	}
	if v != 0 {
		return 0, fmt.Errorf("netmask %q is not contiguous", mask)
	}
	return bits, nil
}

func maskString(bits int) string {
	v := ^uint32(0) << (32 - bits)
	if bits == 0 {
		v = 0
	}
	return fmt.Sprintf("%d.%d.%d.%d", v>>24, v>>16&0xff, v>>8&0xff, v&0xff)
}

func lastAddr(p netip.Prefix) netip.Addr {
	a := p.Masked().Addr().As4()
	v := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
	v |= ^(^uint32(0) << (32 - p.Bits()))
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}
