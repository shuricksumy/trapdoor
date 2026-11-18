// Package firewall turns trapdoor's access rules for VPN clients into an
// iptables chain (TD-FWD, jumped to from INPUT and FORWARD for traffic from
// tun devices) and applies it, either directly or through a file that the
// OpenVPN container applies (Docker: only that container has NET_ADMIN).
package firewall

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/shuricksumy/trapdoor/internal/api"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

// Chain is the iptables chain trapdoor owns.
const Chain = "TD-FWD"

// Actions and defaults.
const (
	Allow = "allow"
	Deny  = "deny"
)

// Protocols.
const (
	ProtoAny    = "any"
	ProtoTCP    = "tcp"
	ProtoUDP    = "udp"
	ProtoTCPUDP = "tcp+udp"
	ProtoICMP   = "icmp"
)

// Policy is the whole access configuration (defined in api).
type Policy = api.FirewallPolicy

// Rule matches traffic from a client (or all clients) to a network. The
// first matching rule decides.
type Rule = api.FirewallRule

// ErrInvalid marks policies that cannot be applied.
var ErrInvalid = errors.New("invalid access rule")

// MaxRules keeps the chain and the UI manageable.
const MaxRules = 500

// Normalize fills defaults and canonical forms (e.g. "10.0.0.5" → "10.0.0.5/32").
func Normalize(p *Policy) {
	if p.Default == "" {
		p.Default = Allow
	}
	if p.Rules == nil {
		p.Rules = []Rule{}
	}
	for i := range p.Rules {
		r := &p.Rules[i]
		r.Client = strings.TrimSpace(r.Client)
		r.To = strings.TrimSpace(r.To)
		r.Ports = strings.ReplaceAll(strings.TrimSpace(r.Ports), " ", "")
		r.Note = strings.TrimSpace(r.Note)
		if r.Proto == "" {
			r.Proto = ProtoAny
		}
		if pr, err := parseTo(r.To); err == nil && pr.IsValid() {
			r.To = pr.Masked().String()
		}
	}
}

// Validate checks a normalized policy. validName checks client names.
func Validate(p Policy, validName func(string) bool) error {
	bad := func(i int, format string, a ...any) error {
		return fmt.Errorf("%w: rule %d: "+format, append([]any{ErrInvalid, i + 1}, a...)...)
	}
	if p.Default != Allow && p.Default != Deny {
		return fmt.Errorf("%w: default must be allow or deny", ErrInvalid)
	}
	if len(p.Rules) > MaxRules {
		return fmt.Errorf("%w: at most %d rules", ErrInvalid, MaxRules)
	}
	for i, r := range p.Rules {
		if r.Client != "" && !validName(r.Client) {
			return bad(i, "%q is not a client name", r.Client)
		}
		if _, err := parseTo(r.To); err != nil {
			return bad(i, "%v", err)
		}
		switch r.Proto {
		case ProtoAny, ProtoICMP:
			if r.Ports != "" {
				return bad(i, "ports need tcp or udp")
			}
		case ProtoTCP, ProtoUDP, ProtoTCPUDP:
			if _, err := parsePorts(r.Ports); err != nil {
				return bad(i, "%v", err)
			}
		default:
			return bad(i, "protocol must be any, tcp, udp, tcp+udp or icmp")
		}
		if r.Action != Allow && r.Action != Deny {
			return bad(i, "action must be allow or deny")
		}
		if len(r.Note) > 200 || strings.ContainsAny(r.Note, "\n\r") {
			return bad(i, "note must be one line of at most 200 characters")
		}
	}
	return nil
}

func parseTo(s string) (netip.Prefix, error) {
	if s == "" {
		return netip.Prefix{}, nil
	}
	if a, err := netip.ParseAddr(s); err == nil && a.Is4() {
		return netip.PrefixFrom(a, 32), nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil || !p.Addr().Is4() {
		return netip.Prefix{}, fmt.Errorf("%q is not an IPv4 network like 192.168.1.0/24 or an address", s)
	}
	return p, nil
}

// parsePorts returns the multiport list ("80,443,8000:8100"), "" for all.
func parsePorts(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	parts := strings.Split(s, ",")
	count := 0
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		lo, hi, isRange := strings.Cut(part, "-")
		a, err1 := strconv.Atoi(lo)
		b := a
		var err2 error
		if isRange {
			b, err2 = strconv.Atoi(hi)
			count++ // a range takes two of the 15 multiport slots
		}
		if err1 != nil || err2 != nil || a < 1 || b > 65535 || a > b {
			return "", fmt.Errorf("%q is not a port or range (1-65535)", part)
		}
		count++
		if isRange {
			out = append(out, fmt.Sprintf("%d:%d", a, b))
		} else {
			out = append(out, strconv.Itoa(a))
		}
	}
	if count > 15 {
		return "", errors.New("at most 15 ports (a range counts as two)")
	}
	return strings.Join(out, ","), nil
}

// Clients is what rendering needs to know about clients.
type Clients struct {
	IPs            map[string]netip.Addr // static VPN address per client
	ClientToClient bool                  // server.conf: traffic between clients bypasses the kernel
}

// Result is a rendered policy.
type Result struct {
	Rules    string   // iptables-restore input (--noflush)
	Warnings []string // e.g. rules skipped because a client has no fixed address
	Missing  []string // clients named in rules without a static address
}

// Hash identifies rendered rules (also computed by the OpenVPN container
// with sha256sum).
func Hash(rules string) string {
	sum := sha256.Sum256([]byte(rules))
	return hex.EncodeToString(sum[:])
}

// Render builds the OpenVPN chain (Chain) for a normalized, valid policy.
func Render(p Policy, c Clients) Result { return RenderChain(Chain, p, c) }

// RenderChain builds the named chain for a normalized, valid policy.
func RenderChain(chain string, p Policy, c Clients) Result {
	var b strings.Builder
	var res Result
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	w("# trapdoor access rules for VPN clients; written by the agent, do not edit")
	w("*filter")
	w(":%s - [0:0]", chain)
	w("-F %s", chain)
	if !p.Enabled {
		w("-A %s -j RETURN", chain)
		w("COMMIT")
		res.Rules = b.String()
		return res
	}
	w("-A %s -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT", chain)
	missing := map[string]bool{}
	for i, r := range p.Rules {
		var src string
		if r.Client != "" {
			ip, ok := c.IPs[r.Client]
			if !ok {
				if !missing[r.Client] {
					missing[r.Client] = true
					res.Missing = append(res.Missing, r.Client)
				}
				res.Warnings = append(res.Warnings, fmt.Sprintf("rule %d is not active: %s has no fixed VPN address", i+1, r.Client))
				continue
			}
			src = " -s " + ip.String() + "/32"
		}
		dst := ""
		if r.To != "" {
			dst = " -d " + r.To
		}
		target := "ACCEPT"
		if r.Action == Deny {
			target = "REJECT"
		}
		ports, _ := parsePorts(r.Ports)
		protos := map[string][]string{ProtoAny: {""}, ProtoICMP: {"icmp"}, ProtoTCP: {"tcp"}, ProtoUDP: {"udp"}, ProtoTCPUDP: {"tcp", "udp"}}[r.Proto]
		for _, pr := range protos {
			m := ""
			if pr != "" {
				m = " -p " + pr
				if ports != "" {
					m += " -m multiport --dports " + ports
				}
			}
			reject := ""
			if target == "REJECT" {
				reject = " --reject-with icmp-admin-prohibited"
				if pr == "tcp" {
					reject = " --reject-with tcp-reset"
				}
			}
			w("-A %s%s%s%s -j %s%s", chain, src, dst, m, target, reject)
		}
	}
	if p.Default == Deny {
		w("-A %s -j REJECT --reject-with icmp-admin-prohibited", chain)
	} else {
		w("-A %s -j RETURN", chain)
	}
	w("COMMIT")
	sort.Strings(res.Missing)
	if c.ClientToClient {
		res.Warnings = append(res.Warnings, "server.conf has client-to-client: OpenVPN passes traffic between clients itself, so these rules do not apply to it")
	}
	res.Rules = b.String()
	return res
}
