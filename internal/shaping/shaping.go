// Package shaping renders a server's speed limits as a shell script of tc
// commands (Linux traffic control) for its VPN device.
//
// Download (server → client) is shaped where packets leave on the device:
// an HTB qdisc with one class per limited client, matched by its VPN
// address. Upload (client → server) arrives on the device, where only
// policing is possible, so it is redirected to an IFB device and shaped
// there the same way. Unlimited clients get no class: HTB sends
// unclassified packets straight on. A blocked client's packets are dropped
// in both directions (it stays connected, nothing gets through).
//
// The script replaces whatever was there, so applying it again (after the
// device was recreated, e.g. a restart) gives the same state. It takes the
// device as its first argument; "clear" as the second only removes.
package shaping

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"
)

// Limit is one client's speed in kbit/s (0 = unlimited) or blocked.
type Limit struct {
	Down, Up int64
	Block    bool
}

// Limited reports whether the limit does anything.
func (l Limit) Limited() bool { return l.Block || l.Down > 0 || l.Up > 0 }

// Client is a client with an address and its limit.
type Client struct {
	Name  string
	Addr  netip.Addr
	Limit Limit
}

// MaxRate is the highest limit accepted, in kbit/s (10 Gbit/s).
const MaxRate = 10_000_000

var ifbName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,14}$`)

// IFB returns the name of the IFB device for a server id (at most 15
// characters, as Linux allows).
func IFB(id string) string {
	n := "tq-" + strings.ToLower(id)
	if len(n) > 15 {
		n = n[:15]
	}
	return n
}

// Render returns the script for the clients (IPv4 addresses only; others
// are left out and returned in skipped).
func Render(iface, ifb string, clients []Client) (script string, skipped []string) {
	if !ifbName.MatchString(ifb) {
		ifb = "tq-vpn"
	}
	var down, up, blocked []Client
	sorted := append([]Client(nil), clients...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Addr.Less(sorted[j].Addr) })
	for _, c := range sorted {
		if !c.Limit.Limited() {
			continue
		}
		if !c.Addr.Is4() {
			skipped = append(skipped, c.Name)
			continue
		}
		switch {
		case c.Limit.Block:
			blocked = append(blocked, c)
		default:
			if c.Limit.Down > 0 {
				down = append(down, c)
			}
			if c.Limit.Up > 0 {
				up = append(up, c)
			}
		}
	}

	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# trapdoor speed limits, made by the agent: sh <this file> <device> [clear]\n")
	fmt.Fprintf(&b, "# IFACE=%s\n", iface)
	b.WriteString("set -e\n")
	b.WriteString("DEV=${1:?device}\n")
	fmt.Fprintf(&b, "IFB=%s\n", ifb)
	b.WriteString("tc qdisc del dev \"$DEV\" root 2>/dev/null || true\n")
	b.WriteString("tc qdisc del dev \"$DEV\" ingress 2>/dev/null || true\n")
	b.WriteString("ip link del \"$IFB\" 2>/dev/null || true\n")
	b.WriteString("[ \"${2:-}\" = clear ] && exit 0\n")
	if len(down)+len(blocked) > 0 {
		b.WriteString("\n# download: server → client\n")
		b.WriteString("tc qdisc add dev \"$DEV\" root handle 1: htb default 0\n")
		for _, c := range blocked {
			fmt.Fprintf(&b, "tc filter add dev \"$DEV\" parent 1: protocol ip prio 1 u32 match ip dst %s/32 action drop # %s blocked\n", c.Addr, comment(c.Name))
		}
		for i, c := range down {
			classes(&b, "$DEV", i, c, c.Limit.Down, "dst")
		}
	}
	if len(up)+len(blocked) > 0 {
		b.WriteString("\n# upload: client → server, through the IFB device\n")
		b.WriteString("tc qdisc add dev \"$DEV\" handle ffff: ingress\n")
		for _, c := range blocked {
			fmt.Fprintf(&b, "tc filter add dev \"$DEV\" parent ffff: protocol ip prio 1 u32 match ip src %s/32 action drop # %s blocked\n", c.Addr, comment(c.Name))
		}
		if len(up) > 0 {
			b.WriteString("ip link add \"$IFB\" type ifb\n")
			b.WriteString("ip link set \"$IFB\" up\n")
			b.WriteString("tc qdisc add dev \"$IFB\" root handle 1: htb default 0\n")
			for i, c := range up {
				classes(&b, "$IFB", i, c, c.Limit.Up, "src")
			}
			b.WriteString("tc filter add dev \"$DEV\" parent ffff: protocol ip prio 2 u32 match u32 0 0 action mirred egress redirect dev \"$IFB\"\n")
		}
	}
	return b.String(), skipped
}

// classes adds the HTB class, its queue and the filter for one client.
func classes(b *strings.Builder, dev string, i int, c Client, kbit int64, dir string) {
	id := fmt.Sprintf("1:%x", 0x10+i)
	fmt.Fprintf(b, "tc class add dev \"%s\" parent 1: classid %s htb rate %dkbit ceil %dkbit quantum %d # %s\n", dev, id, kbit, kbit, quantum(kbit), comment(c.Name))
	fmt.Fprintf(b, "tc qdisc add dev \"%s\" parent %s fq_codel\n", dev, id)
	fmt.Fprintf(b, "tc filter add dev \"%s\" parent 1: protocol ip prio 2 u32 match ip %s %s/32 flowid %s\n", dev, dir, c.Addr, id)
}

// quantum: bytes a class may send per round; a tenth of a second of its
// rate, at least one full packet and at most what HTB accepts quietly.
func quantum(kbit int64) int64 {
	return min(max(kbit*1000/8/10, 1514), 200000)
}

// comment keeps a client name safe inside a shell comment.
func comment(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// Hash is the sha256 of a script, as the VPN container reports it.
func Hash(script string) string {
	sum := sha256.Sum256([]byte(script))
	return hex.EncodeToString(sum[:])
}
