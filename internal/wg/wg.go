// Package wg holds the WireGuard pieces that need no privileges: key
// pairs, wg-quick style configuration files for the server and its clients,
// and parsing of `wg show <iface> dump`. AmneziaWG uses the same format with
// extra [Interface] settings (Extra).
package wg

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// KeyLen is the length of a WireGuard key in bytes.
const KeyLen = 32

// GenerateKey returns a new private key and its public key, base64 encoded
// as wg(8) prints them.
func GenerateKey() (priv, pub string, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(k.Bytes()),
		base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

// PublicKey returns the public key of a base64 private key.
func PublicKey(priv string) (string, error) {
	b, err := decodeKey(priv)
	if err != nil {
		return "", err
	}
	k, err := ecdh.X25519().NewPrivateKey(b)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

// GeneratePSK returns a new preshared key.
func GeneratePSK() (string, error) {
	b := make([]byte, KeyLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// ValidKey reports whether s is a base64 WireGuard key.
func ValidKey(s string) bool {
	_, err := decodeKey(s)
	return err == nil
}

func decodeKey(s string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != KeyLen {
		return nil, errors.New("not a WireGuard key")
	}
	return b, nil
}

// Setting is one extra "Key = Value" line in [Interface], in order
// (AmneziaWG's Jc, Jmin, S1, H1 …).
type Setting struct {
	Key, Value string
}

// Server is the server side of a tunnel.
type Server struct {
	PrivateKey string
	Address    []netip.Prefix // the server's addresses, e.g. 10.9.0.1/24
	ListenPort int
	MTU        int // 0 = default
	Extra      []Setting
}

// Peer is a client as the server sees it.
type Peer struct {
	Name         string // written as a comment; WireGuard itself has no names
	PublicKey    string
	PresharedKey string         // optional
	AllowedIPs   []netip.Prefix // the client's addresses plus LANs behind it
}

// ServerConfig renders the server's configuration file (wg-quick format;
// `wg-quick strip` turns it into what `wg syncconf` reads).
func ServerConfig(s Server, peers []Peer) string {
	var b strings.Builder
	b.WriteString("# Written by trapdoor; changes made here are overwritten.\n[Interface]\n")
	kv(&b, "PrivateKey", s.PrivateKey)
	kv(&b, "Address", prefixes(s.Address))
	if s.ListenPort > 0 {
		kv(&b, "ListenPort", strconv.Itoa(s.ListenPort))
	}
	if s.MTU > 0 {
		kv(&b, "MTU", strconv.Itoa(s.MTU))
	}
	for _, e := range s.Extra {
		kv(&b, e.Key, e.Value)
	}
	for _, p := range peers {
		b.WriteString("\n")
		if p.Name != "" {
			b.WriteString("# " + oneLine(p.Name) + "\n")
		}
		b.WriteString("[Peer]\n")
		kv(&b, "PublicKey", p.PublicKey)
		if p.PresharedKey != "" {
			kv(&b, "PresharedKey", p.PresharedKey)
		}
		kv(&b, "AllowedIPs", prefixes(p.AllowedIPs))
	}
	return b.String()
}

// Client is everything a client's configuration file needs.
type Client struct {
	PrivateKey   string
	Address      []netip.Prefix
	DNS          []string
	MTU          int
	Extra        []Setting // must match the server's for AmneziaWG
	ServerKey    string    // the server's public key
	PresharedKey string
	Endpoint     string         // host:port
	AllowedIPs   []netip.Prefix // what goes through the tunnel
	Keepalive    int            // seconds; 0 = off
}

// ClientConfig renders a client's configuration file, as imported by the
// WireGuard (or Amnezia) apps.
func ClientConfig(c Client) string {
	var b strings.Builder
	b.WriteString("[Interface]\n")
	kv(&b, "PrivateKey", c.PrivateKey)
	kv(&b, "Address", prefixes(c.Address))
	if len(c.DNS) > 0 {
		kv(&b, "DNS", strings.Join(c.DNS, ", "))
	}
	if c.MTU > 0 {
		kv(&b, "MTU", strconv.Itoa(c.MTU))
	}
	for _, e := range c.Extra {
		kv(&b, e.Key, e.Value)
	}
	b.WriteString("\n[Peer]\n")
	kv(&b, "PublicKey", c.ServerKey)
	if c.PresharedKey != "" {
		kv(&b, "PresharedKey", c.PresharedKey)
	}
	kv(&b, "Endpoint", c.Endpoint)
	kv(&b, "AllowedIPs", prefixes(c.AllowedIPs))
	if c.Keepalive > 0 {
		kv(&b, "PersistentKeepalive", strconv.Itoa(c.Keepalive))
	}
	return b.String()
}

func kv(b *strings.Builder, k, v string) {
	b.WriteString(k + " = " + oneLine(v) + "\n")
}

// oneLine keeps a value from adding lines or sections to the file.
func oneLine(s string) string {
	return strings.NewReplacer("\n", " ", "\r", " ").Replace(s)
}

func prefixes(ps []netip.Prefix) string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.String()
	}
	return strings.Join(out, ", ")
}

// DumpPeer is one peer line of `wg show <iface> dump`.
type DumpPeer struct {
	PublicKey     string
	Endpoint      string // "" when the peer never connected
	AllowedIPs    []netip.Prefix
	LastHandshake time.Time // zero = never
	RxBytes       int64     // received from the peer
	TxBytes       int64     // sent to the peer
}

// Dump is the parsed output of `wg show <iface> dump`.
type Dump struct {
	PublicKey  string
	ListenPort int
	Peers      []DumpPeer
}

// ParseDump parses `wg show <iface> dump`: the interface line (private key,
// public key, port, fwmark) and one tab-separated line per peer (public
// key, preshared key, endpoint, allowed ips, latest handshake, rx, tx,
// keepalive). AmneziaWG's `awg show` prints the same columns.
func ParseDump(out string) (Dump, error) {
	var d Dump
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return d, errors.New("empty wg dump")
	}
	f := strings.Split(lines[0], "\t")
	if len(f) < 4 {
		return d, fmt.Errorf("wg dump: bad interface line %q", lines[0])
	}
	d.PublicKey = f[1]
	d.ListenPort, _ = strconv.Atoi(f[2])
	for _, line := range lines[1:] {
		f := strings.Split(line, "\t")
		if len(f) < 8 {
			return d, fmt.Errorf("wg dump: bad peer line %q", line)
		}
		p := DumpPeer{PublicKey: f[0]}
		if f[2] != "(none)" {
			p.Endpoint = f[2]
		}
		if f[3] != "(none)" {
			for _, s := range strings.Split(f[3], ",") {
				if pfx, err := netip.ParsePrefix(s); err == nil {
					p.AllowedIPs = append(p.AllowedIPs, pfx)
				}
			}
		}
		if ts, _ := strconv.ParseInt(f[4], 10, 64); ts > 0 {
			p.LastHandshake = time.Unix(ts, 0).UTC()
		}
		p.RxBytes, _ = strconv.ParseInt(f[5], 10, 64)
		p.TxBytes, _ = strconv.ParseInt(f[6], 10, 64)
		d.Peers = append(d.Peers, p)
	}
	return d, nil
}

// ConnectedWithin is how recent a handshake must be for a peer to count as
// connected. WireGuard renews the handshake every 2 minutes while traffic
// flows, and a keepalive keeps it going when idle.
const ConnectedWithin = 3 * time.Minute

// Connected reports whether the peer had a handshake recently.
func (p DumpPeer) Connected(now time.Time) bool {
	return !p.LastHandshake.IsZero() && now.Sub(p.LastHandshake) < ConnectedWithin
}
