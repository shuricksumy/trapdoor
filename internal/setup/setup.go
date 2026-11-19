// Package setup creates a new OpenVPN server configuration: it validates the
// wizard's answers and renders server.conf and client-template.txt. The agent
// creates the PKI and keys and writes the files.
package setup

import (
	"errors"
	"fmt"
	"github.com/shuricksumy/trapdoor/internal/api"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"
)

// TLS modes for the control channel.
const (
	TLSCryptV2 = "tls-crypt-v2" // per-client keys (recommended)
	TLSCrypt   = "tls-crypt"
	TLSAuth    = "tls-auth"
	TLSNone    = "none"
)

// Request is what the wizard asks (defined in api for the web side).
type Request = api.SetupRequest

// ErrInvalid marks wizard answers that cannot be used.
var ErrInvalid = errors.New("invalid setup")

var hostRe = regexp.MustCompile(`^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)*[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// Defaults fills empty fields.
func Defaults(r *Request) {
	if r.Port == 0 {
		r.Port = 1194
	}
	if r.PublicPort == 0 {
		r.PublicPort = r.Port
	}
	if r.Proto == "" {
		r.Proto = "udp"
	}
	if r.Subnet == "" {
		r.Subnet = "10.8.0.0/24"
	}
	if r.CAName == "" {
		r.CAName = "trapdoor CA"
	}
	if r.KeyType == "" {
		r.KeyType = "ec256"
	}
	if r.TLS == "" {
		r.TLS = TLSCryptV2
	}
	if r.DNS == nil {
		r.DNS = []string{}
	}
}

// Validate checks the answers (after Defaults).
func Validate(r Request) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, a...)...)
	}
	h := strings.TrimSpace(r.PublicHost)
	if h == "" {
		return bad("the public address is required (the hostname or IP clients connect to)")
	}
	if _, err := netip.ParseAddr(h); err != nil && !hostRe.MatchString(h) {
		return bad("%q is not a hostname or IP address", h)
	}
	for _, p := range []int{r.Port, r.PublicPort} {
		if p < 1 || p > 65535 {
			return bad("ports must be 1-65535")
		}
	}
	if r.Proto != "udp" && r.Proto != "tcp" {
		return bad("protocol must be udp or tcp")
	}
	p, err := netip.ParsePrefix(r.Subnet)
	if err != nil || !p.Addr().Is4() || p != p.Masked() || p.Bits() < 8 || p.Bits() > 29 {
		return bad("VPN subnet must be an IPv4 network like 10.8.0.0/24 (between /8 and /29)")
	}
	if !p.Addr().IsPrivate() {
		return bad("VPN subnet %s is not a private network (use 10.x, 172.16-31.x or 192.168.x)", p)
	}
	for _, d := range r.DNS {
		if _, err := netip.ParseAddr(d); err != nil {
			return bad("DNS server %q is not an IP address", d)
		}
	}
	if l := len(r.CAName); l == 0 || l > 64 || strings.ContainsAny(r.CAName, "\n\r/") {
		return bad("CA name must be 1-64 characters without / or line breaks")
	}
	if !slices.Contains([]string{"ec256", "ec384", "rsa2048", "rsa3072", "rsa4096"}, r.KeyType) {
		return bad("key type must be ec256, ec384, rsa2048, rsa3072 or rsa4096")
	}
	if !slices.Contains([]string{TLSCryptV2, TLSCrypt, TLSAuth, TLSNone}, r.TLS) {
		return bad("control channel must be tls-crypt-v2, tls-crypt, tls-auth or none")
	}
	return nil
}

// Paths are where the generated files point to, relative to the OpenVPN dir.
type Paths struct {
	CA, Cert, Key string // e.g. easy-rsa/pki/ca.crt
	CRL           string // e.g. crl.pem
	TLSKey        string // e.g. tls-crypt-v2.key ("" for none)
	CCD           string // e.g. ccd
	Management    string // "127.0.0.1 2080"
	AuthVerify    string // command, e.g. /usr/local/bin/trapdoor auth-verify --ccd /etc/openvpn/ccd
	Group         string // group to drop to: nogroup or nobody; "" keeps root
	Dev           string // tun device: "" or "tun" (tun0, tun1, …), or a name
}

const dataCiphers = "AES-256-GCM:AES-128-GCM:CHACHA20-POLY1305"

// ServerConf renders server.conf.
func ServerConf(r Request, p Paths, now time.Time) string {
	sub := netip.MustParsePrefix(r.Subnet)
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	w("# OpenVPN server, created by the trapdoor setup on %s.", now.UTC().Format("2006-01-02"))
	w("# Edit it on the Server page; every change is checked and backed up.")
	w("")
	w("port %d", r.Port)
	if r.Proto == "tcp" {
		w("proto tcp-server")
	} else {
		w("proto udp")
		w("explicit-exit-notify 1")
	}
	if p.Dev == "" || p.Dev == "tun" {
		w("dev tun")
	} else {
		w("dev %s", p.Dev)
		w("dev-type tun")
	}
	w("topology subnet")
	w("server %s %s", sub.Addr(), mask(sub.Bits()))
	w("keepalive 10 60")
	w("persist-key")
	w("persist-tun")
	if p.Group != "" {
		w("user nobody")
		w("group %s", p.Group)
	}
	w("")
	w("ca %s", p.CA)
	w("cert %s", p.Cert)
	w("key %s", p.Key)
	w("dh none")
	w("crl-verify %s", p.CRL)
	switch r.TLS {
	case TLSCryptV2:
		w("tls-crypt-v2 %s", p.TLSKey)
	case TLSCrypt:
		w("tls-crypt %s", p.TLSKey)
	case TLSAuth:
		w("tls-auth %s 0", p.TLSKey)
	}
	w("tls-version-min 1.2")
	w("data-ciphers %s", dataCiphers)
	w("")
	w("client-config-dir %s", p.CCD)
	if r.ClientToClient {
		w("client-to-client")
	}
	if r.FullTunnel {
		w(`push "redirect-gateway def1 bypass-dhcp"`)
	}
	for _, d := range r.DNS {
		w(`push "dhcp-option DNS %s"`, d)
	}
	if r.TwoFactor {
		w("")
		w("# Two-factor login: username = client name, password = static password + code")
		w("script-security 2")
		w(`auth-user-pass-verify "%s" via-file`, p.AuthVerify)
	}
	w("")
	w("# Used by trapdoor for live sessions and restarts; keep it on localhost.")
	w("management %s", p.Management)
	w("verb 3")
	return b.String()
}

// ClientTemplate renders client-template.txt (the start of every .ovpn).
func ClientTemplate(r Request) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	w("client")
	w("dev tun")
	w("proto %s", r.Proto)
	w("remote %s %d", strings.TrimSpace(r.PublicHost), r.PublicPort)
	w("resolv-retry infinite")
	w("nobind")
	w("persist-key")
	w("persist-tun")
	w("remote-cert-tls server")
	w("verify-x509-name server name")
	w("tls-version-min 1.2")
	w("data-ciphers %s", dataCiphers)
	if r.Proto == "udp" {
		w("explicit-exit-notify")
	}
	if r.TwoFactor {
		w("auth-user-pass")
		w("auth-nocache")
	}
	w("verb 3")
	return b.String()
}

func mask(bits int) string {
	v := ^uint32(0) << (32 - bits)
	return fmt.Sprintf("%d.%d.%d.%d", v>>24, v>>16&0xff, v>>8&0xff, v&0xff)
}
