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

// Limit is one client's speed in kbit/s (0 = unlimited) or blocked, and
// its priority ("high", "low"; "" = normal).
type Limit struct {
	Down, Up int64
	Block    bool
	Prio     string
}

// Limited reports whether the limit does anything without priorities.
func (l Limit) Limited() bool { return l.Block || l.Down > 0 || l.Up > 0 }

// Client is a client with an address and its limit.
type Client struct {
	Name  string
	Addr  netip.Addr
	Limit Limit
}

// Link is the server's speed in kbit/s each way, for priorities: traffic
// is kept just under it, so a queue forms here and high-priority clients
// go first. 0 = no priorities that way.
type Link struct {
	Down, Up int64
}

// weights: a client's guaranteed share of the link by priority.
var weights = map[string]int64{"high": 4, "": 2, "low": 1}

// prios: HTB lends spare bandwidth to lower numbers first.
var prios = map[string]int{"high": 0, "": 1, "low": 2}

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
func Render(iface, ifb string, clients []Client, link Link) (script string, skipped []string) {
	if !ifbName.MatchString(ifb) {
		ifb = "tq-vpn"
	}
	var active, blocked []Client
	sorted := append([]Client(nil), clients...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Addr.Less(sorted[j].Addr) })
	for _, c := range sorted {
		if !c.Limit.Limited() && link == (Link{}) {
			continue
		}
		if !c.Addr.Is4() {
			if c.Limit.Limited() {
				skipped = append(skipped, c.Name)
			}
			continue
		}
		if c.Limit.Block {
			blocked = append(blocked, c)
		} else {
			active = append(active, c)
		}
	}
	down := direction{dev: "$DEV", match: "dst", link: link.Down, rate: func(l Limit) int64 { return l.Down }}
	up := direction{dev: "$IFB", match: "src", link: link.Up, rate: func(l Limit) int64 { return l.Up }}

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
	b.WriteString("if [ \"${2:-}\" = clear ]; then exit 0; fi\n")
	if down.needed(active) || len(blocked) > 0 {
		b.WriteString("\n# download: server → client\n")
		down.root(&b)
		for _, c := range blocked {
			fmt.Fprintf(&b, "tc filter add dev \"$DEV\" parent 1: protocol ip prio 1 u32 match ip dst %s/32 action drop # %s blocked\n", c.Addr, comment(c.Name))
		}
		down.classes(&b, active)
	}
	if up.needed(active) || len(blocked) > 0 {
		b.WriteString("\n# upload: client → server, through the IFB device\n")
		b.WriteString("tc qdisc add dev \"$DEV\" handle ffff: ingress\n")
		for _, c := range blocked {
			fmt.Fprintf(&b, "tc filter add dev \"$DEV\" parent ffff: protocol ip prio 1 u32 match ip src %s/32 action drop # %s blocked\n", c.Addr, comment(c.Name))
		}
		if up.needed(active) {
			b.WriteString("ip link add \"$IFB\" type ifb\n")
			b.WriteString("ip link set \"$IFB\" up\n")
			up.root(&b)
			up.classes(&b, active)
			b.WriteString("tc filter add dev \"$DEV\" parent ffff: protocol ip prio 2 u32 match u32 0 0 action mirred egress redirect dev \"$IFB\"\n")
		}
	}
	b.WriteString("exit 0\n")
	return b.String(), skipped
}

// direction renders one way: downloads leave on the device, uploads on
// the IFB device.
type direction struct {
	dev, match string
	link       int64 // 0: no priorities, only limited clients get a class
	rate       func(Limit) int64
}

func (d direction) needed(cs []Client) bool {
	if d.link > 0 {
		return true
	}
	for _, c := range cs {
		if d.rate(c.Limit) > 0 {
			return true
		}
	}
	return false
}

// root: without a link, unclassified traffic passes untouched (default 0);
// with one, everything goes through 1:1 at the link's speed and traffic of
// unknown addresses into 1:2.
func (d direction) root(b *strings.Builder) {
	if d.link == 0 {
		fmt.Fprintf(b, "tc qdisc add dev \"%s\" root handle 1: htb default 0\n", d.dev)
		return
	}
	fmt.Fprintf(b, "tc qdisc add dev \"%s\" root handle 1: htb default 2\n", d.dev)
	fmt.Fprintf(b, "tc class add dev \"%s\" parent 1: classid 1:1 htb rate %dkbit ceil %dkbit quantum %d # the link\n", d.dev, d.link, d.link, quantum(d.link))
}

func (d direction) classes(b *strings.Builder, cs []Client) {
	if d.link == 0 {
		i := 0
		for _, c := range cs {
			if k := d.rate(c.Limit); k > 0 {
				id := fmt.Sprintf("1:%x", 0x10+i)
				i++
				fmt.Fprintf(b, "tc class add dev \"%s\" parent 1: classid %s htb rate %dkbit ceil %dkbit quantum %d # %s\n", d.dev, id, k, k, quantum(k), comment(c.Name))
				d.leaf(b, id, c)
			}
		}
		return
	}
	// With a link every client has a class: a guaranteed share by priority
	// (high 4, normal 2, low 1; the rest of the traffic counts as normal),
	// at most its own limit or the link, and spare bandwidth to high first.
	total := weights[""]
	for _, c := range cs {
		total += weights[prioOf(c)]
	}
	share := func(p string, ceil int64) int64 { return min(max(d.link*weights[p]/total, 8), ceil) }
	fmt.Fprintf(b, "tc class add dev \"%s\" parent 1:1 classid 1:2 htb rate %dkbit ceil %dkbit prio 1 quantum %d # everything else\n", d.dev, share("", d.link), d.link, quantum(d.link))
	fmt.Fprintf(b, "tc qdisc add dev \"%s\" parent 1:2 fq_codel\n", d.dev)
	for i, c := range cs {
		id := fmt.Sprintf("1:%x", 0x10+i)
		ceil := d.link
		if k := d.rate(c.Limit); k > 0 {
			ceil = min(k, d.link)
		}
		p := prioOf(c)
		fmt.Fprintf(b, "tc class add dev \"%s\" parent 1:1 classid %s htb rate %dkbit ceil %dkbit prio %d quantum %d # %s\n", d.dev, id, share(p, ceil), ceil, prios[p], quantum(ceil), comment(c.Name))
		d.leaf(b, id, c)
	}
}

func (d direction) leaf(b *strings.Builder, id string, c Client) {
	fmt.Fprintf(b, "tc qdisc add dev \"%s\" parent %s fq_codel\n", d.dev, id)
	fmt.Fprintf(b, "tc filter add dev \"%s\" parent 1: protocol ip prio 2 u32 match ip %s %s/32 flowid %s\n", d.dev, d.match, c.Addr, id)
}

func prioOf(c Client) string {
	if _, ok := weights[c.Limit.Prio]; ok {
		return c.Limit.Prio
	}
	return ""
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
