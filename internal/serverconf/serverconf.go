// Package serverconf edits OpenVPN's server.conf: a form-level view of the
// common options (Settings), line-preserving updates (Apply), and checks
// before anything is written (Check, which also asks the openvpn binary).
package serverconf

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Settings are the options the UI edits as a form. Everything else in
// server.conf is kept as it is (and can be edited as raw text).
type Settings struct {
	Port           int      `json:"port"`
	Proto          string   `json:"proto"`    // udp, tcp, udp6, tcp6 (tcp = tcp-server)
	Subnet         string   `json:"subnet"`   // "server" line as CIDR, e.g. 10.8.0.0/24
	Topology       string   `json:"topology"` // subnet, net30, p2p
	DNS            []string `json:"dns"`      // push "dhcp-option DNS …"
	Domain         string   `json:"domain"`   // push "dhcp-option DOMAIN …"
	FullTunnel     bool     `json:"full_tunnel"`
	ClientToClient bool     `json:"client_to_client"`
	DuplicateCN    bool     `json:"duplicate_cn"`
	MaxClients     int      `json:"max_clients"` // 0 = no limit
	Routes         []string `json:"routes"`      // "route" lines: LANs reached through clients
	PushRoutes     []string `json:"push_routes"` // push "route …" to every client
}

// line is one config line, parsed if it is an active directive.
type line struct {
	text string
	name string   // directive name, "" for comments/blank
	args []string // for push: the quoted argument split into fields
}

func parseLines(raw string) []line {
	var out []line
	for _, t := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		l := line{text: t}
		s := strings.TrimSpace(t)
		if s != "" && s[0] != '#' && s[0] != ';' {
			name, rest, _ := strings.Cut(s, " ")
			l.name = name
			rest = strings.TrimSpace(rest)
			if name == "push" {
				rest = strings.Trim(rest, `"`)
			}
			l.args = strings.Fields(rest)
		}
		out = append(out, l)
	}
	return out
}

func (l line) push(kind ...string) bool {
	if l.name != "push" || len(l.args) < len(kind) {
		return false
	}
	for i, k := range kind {
		if l.args[i] != k {
			return false
		}
	}
	return true
}

// Parse reads the form-level settings from server.conf.
func Parse(raw string) Settings {
	s := Settings{Topology: "net30", DNS: []string{}, Routes: []string{}, PushRoutes: []string{}}
	for _, l := range parseLines(raw) {
		switch {
		case l.name == "port" && len(l.args) > 0:
			s.Port, _ = strconv.Atoi(l.args[0])
		case l.name == "proto" && len(l.args) > 0:
			s.Proto = strings.TrimSuffix(l.args[0], "-server")
		case l.name == "server" && len(l.args) >= 2:
			if p, err := toPrefix(l.args[0], l.args[1]); err == nil {
				s.Subnet = p.String()
			}
		case l.name == "topology" && len(l.args) > 0:
			s.Topology = l.args[0]
		case l.name == "client-to-client":
			s.ClientToClient = true
		case l.name == "duplicate-cn":
			s.DuplicateCN = true
		case l.name == "max-clients" && len(l.args) > 0:
			s.MaxClients, _ = strconv.Atoi(l.args[0])
		case l.name == "route" && len(l.args) >= 2:
			if p, err := toPrefix(l.args[0], l.args[1]); err == nil {
				s.Routes = append(s.Routes, p.String())
			}
		case l.push("route") && len(l.args) >= 3:
			if p, err := toPrefix(l.args[1], l.args[2]); err == nil {
				s.PushRoutes = append(s.PushRoutes, p.String())
			}
		case l.push("redirect-gateway"):
			s.FullTunnel = true
		case l.push("dhcp-option", "DNS") && len(l.args) >= 3:
			s.DNS = append(s.DNS, l.args[2])
		case l.push("dhcp-option", "DOMAIN") && len(l.args) >= 3:
			s.Domain = l.args[2]
		}
	}
	if s.Port == 0 {
		s.Port = 1194 // OpenVPN's default
	}
	if s.Proto == "" {
		s.Proto = "udp"
	}
	return s
}

// ErrInvalid marks settings or config text that would not work.
var ErrInvalid = errors.New("invalid server settings")

var domainRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62}\.)*[A-Za-z0-9-]{1,63}$`)

// Validate checks the form values.
func Validate(s Settings) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, a...)...)
	}
	if s.Port < 1 || s.Port > 65535 {
		return bad("port must be 1-65535")
	}
	if !slices.Contains([]string{"udp", "tcp", "udp6", "tcp6", "udp4", "tcp4"}, s.Proto) {
		return bad("proto must be udp or tcp (optionally udp6/tcp6)")
	}
	p, err := netip.ParsePrefix(s.Subnet)
	if err != nil || !p.Addr().Is4() || p != p.Masked() || p.Bits() < 8 || p.Bits() > 29 {
		return bad("VPN subnet must be an IPv4 network like 10.8.0.0/24 (between /8 and /29)")
	}
	if !slices.Contains([]string{"subnet", "net30", "p2p"}, s.Topology) {
		return bad("topology must be subnet, net30 or p2p")
	}
	for _, d := range s.DNS {
		if a, err := netip.ParseAddr(d); err != nil || !a.IsValid() {
			return bad("DNS server %q is not an IP address", d)
		}
	}
	if s.Domain != "" && !domainRe.MatchString(s.Domain) {
		return bad("domain %q is not valid", s.Domain)
	}
	if s.MaxClients < 0 || s.MaxClients > 65535 {
		return bad("max clients must be 0 (no limit) to 65535")
	}
	for _, list := range [][]string{s.Routes, s.PushRoutes} {
		for _, r := range list {
			rp, err := netip.ParsePrefix(r)
			if err != nil || !rp.Addr().Is4() || rp != rp.Masked() {
				return bad("%q is not an IPv4 network like 192.168.1.0/24", r)
			}
			if rp.Overlaps(p) {
				return bad("%s overlaps the VPN subnet %s", r, p)
			}
		}
	}
	return nil
}

// Apply writes s into raw, changing only the lines of the options it
// manages; comments and all other options stay where they are.
func Apply(raw string, s Settings) string {
	lines := parseLines(raw)
	p, _ := netip.ParsePrefix(s.Subnet)
	proto := s.Proto
	if proto == "tcp" || proto == "tcp4" || proto == "tcp6" {
		proto += "-server"
	}

	type rule struct {
		match func(line) bool
		want  []string // replacement lines (may be empty = remove)
	}
	var push []string
	for _, r := range s.PushRoutes {
		rp, _ := netip.ParsePrefix(r)
		push = append(push, fmt.Sprintf(`push "route %s %s"`, rp.Addr(), mask(rp.Bits())))
	}
	var routes []string
	for _, r := range s.Routes {
		rp, _ := netip.ParsePrefix(r)
		routes = append(routes, fmt.Sprintf("route %s %s", rp.Addr(), mask(rp.Bits())))
	}
	var dns []string
	for _, d := range s.DNS {
		dns = append(dns, fmt.Sprintf(`push "dhcp-option DNS %s"`, d))
	}
	one := func(cond bool, l string) []string {
		if cond {
			return []string{l}
		}
		return nil
	}
	rules := []rule{
		{func(l line) bool { return l.name == "port" }, []string{fmt.Sprintf("port %d", s.Port)}},
		{func(l line) bool { return l.name == "proto" }, []string{"proto " + proto}},
		{func(l line) bool { return l.name == "server" }, []string{fmt.Sprintf("server %s %s", p.Addr(), mask(p.Bits()))}},
		{func(l line) bool { return l.name == "topology" }, []string{"topology " + s.Topology}},
		{func(l line) bool { return l.name == "client-to-client" }, one(s.ClientToClient, "client-to-client")},
		{func(l line) bool { return l.name == "duplicate-cn" }, one(s.DuplicateCN, "duplicate-cn")},
		{func(l line) bool { return l.name == "max-clients" }, one(s.MaxClients > 0, fmt.Sprintf("max-clients %d", s.MaxClients))},
		{func(l line) bool { return l.push("redirect-gateway") }, one(s.FullTunnel, `push "redirect-gateway def1 bypass-dhcp"`)},
		{func(l line) bool { return l.push("dhcp-option", "DNS") }, dns},
		{func(l line) bool { return l.push("dhcp-option", "DOMAIN") }, one(s.Domain != "", fmt.Sprintf(`push "dhcp-option DOMAIN %s"`, s.Domain))},
		{func(l line) bool { return l.name == "route" }, routes},
		{func(l line) bool { return l.push("route") }, push},
	}

	out := make([]string, 0, len(lines)+8)
	placed := make([]bool, len(rules))
	for _, l := range lines {
		matched := -1
		for i, r := range rules {
			if l.name != "" && r.match(l) {
				matched = i
				break
			}
		}
		if matched < 0 {
			out = append(out, l.text)
			continue
		}
		// The first occurrence is replaced by the whole new set; later ones are dropped.
		if !placed[matched] {
			out = append(out, rules[matched].want...)
			placed[matched] = true
		}
	}
	var missing []string
	for i, r := range rules {
		if !placed[i] {
			missing = append(missing, r.want...)
		}
	}
	if len(missing) > 0 {
		out = append(out, "", "# Added by trapdoor")
		out = append(out, missing...)
	}
	return strings.Join(out, "\n") + "\n"
}

func toPrefix(addr, m string) (netip.Prefix, error) {
	a, err := netip.ParseAddr(addr)
	if err != nil || !a.Is4() {
		return netip.Prefix{}, fmt.Errorf("bad address %q", addr)
	}
	mm, err := netip.ParseAddr(m)
	if err != nil || !mm.Is4() {
		return netip.Prefix{}, fmt.Errorf("bad netmask %q", m)
	}
	b := mm.As4()
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	bits := 0
	for v&0x80000000 != 0 {
		bits++
		v <<= 1
	}
	if v != 0 {
		return netip.Prefix{}, fmt.Errorf("netmask %q is not contiguous", m)
	}
	return netip.PrefixFrom(a, bits).Masked(), nil
}

func mask(bits int) string {
	v := uint32(0)
	if bits > 0 {
		v = ^uint32(0) << (32 - bits)
	}
	return fmt.Sprintf("%d.%d.%d.%d", v>>24, v>>16&0xff, v>>8&0xff, v&0xff)
}
