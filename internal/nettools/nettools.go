// Package nettools runs simple network diagnostics for the Tools page: ping,
// traceroute, a route lookup, DNS, TCP ports and an HTTP(S) check. The agent
// runs them in the VPN server's network (in Docker the agent shares the vpn
// container's network), so they see what the server sees.
//
// Nothing goes through a shell: ping, traceroute and ip get their arguments
// as a list, and every target is checked first (an IP address or a host
// name), so it cannot be read as an option or a command.
package nettools

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Limits.
const (
	MaxOutput  = 32 << 10 // bytes of output kept
	MaxTimeout = 30 * time.Second
	MaxPorts   = 20
)

// ErrBadInput marks a request that is refused before anything runs.
var ErrBadInput = errors.New("bad input")

func bad(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrBadInput}, a...)...)
}

var hostRe = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*\.?$`)

// Target checks a target: an IPv4 / IPv6 address or a host name. It never
// starts with "-", so it cannot be taken as an option.
func Target(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", bad("enter a host name or an IP address")
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return a.String(), nil
	}
	if len(s) > 253 || !hostRe.MatchString(s) {
		return "", bad("%q is not a host name or an IP address", s)
	}
	return strings.TrimSuffix(s, "."), nil
}

// Result is what a tool found.
type Result struct {
	OK        bool   `json:"ok"`      // the target answered / the check passed
	Summary   string `json:"summary"` // one line, e.g. "4/4 replies, avg 12.3 ms"
	Output    string `json:"output"`  // the full output
	Truncated bool   `json:"truncated,omitempty"`
	Millis    int64  `json:"duration_ms"`
}

// limited collects output up to MaxOutput.
type limited struct {
	bytes.Buffer
	cut bool
}

func (l *limited) Write(p []byte) (int, error) {
	if room := MaxOutput - l.Len(); room < len(p) {
		l.cut = true
		if room > 0 {
			l.Buffer.Write(p[:room])
		}
		return len(p), nil
	}
	return l.Buffer.Write(p)
}

// run starts name with args (no shell) and collects its output.
func run(ctx context.Context, name string, args ...string) (string, bool, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", false, fmt.Errorf("%s is not installed here", name)
	}
	var out limited
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	if ctx.Err() != nil {
		err = errors.New("timed out")
	}
	return out.String(), out.cut, err
}

var pingStats = regexp.MustCompile(`(\d+) packets transmitted, (\d+) (?:packets )?received`)
var pingRTT = regexp.MustCompile(`= ([\d.]+)/([\d.]+)/([\d.]+)`)

// Ping sends count echo requests (1-10).
func Ping(ctx context.Context, target string, count int) (Result, error) {
	t, err := Target(target)
	if err != nil {
		return Result{}, err
	}
	count = min(max(count, 1), 10)
	start := time.Now()
	out, cut, err := run(ctx, "ping", "-c", strconv.Itoa(count), "-W", "2", t)
	r := Result{Output: out, Truncated: cut, Millis: time.Since(start).Milliseconds()}
	if m := pingStats.FindStringSubmatch(out); m != nil {
		sent, _ := strconv.Atoi(m[1])
		got, _ := strconv.Atoi(m[2])
		r.OK = got > 0
		r.Summary = fmt.Sprintf("%d/%d replies", got, sent)
		if rt := pingRTT.FindStringSubmatch(out); rt != nil {
			r.Summary += fmt.Sprintf(", min %s / avg %s / max %s ms", rt[1], rt[2], rt[3])
		}
		if got < sent {
			r.Summary += fmt.Sprintf(", %d%% loss", (sent-got)*100/sent)
		}
		return r, nil
	}
	r.Summary = "no answer"
	if err != nil {
		r.Summary = firstLine(out, err.Error())
	}
	return r, nil
}

var hopRe = regexp.MustCompile(`(?m)^\s*(\d+)\s+(\S+)`)

// Traceroute shows the path, up to 20 hops, one probe each.
func Traceroute(ctx context.Context, target string) (Result, error) {
	t, err := Target(target)
	if err != nil {
		return Result{}, err
	}
	start := time.Now()
	// one probe per hop, 1 s each: at most about 20 s for 20 hops
	out, cut, err := run(ctx, "traceroute", "-n", "-q", "1", "-w", "1", "-m", "20", t)
	r := Result{Output: out, Truncated: cut, Millis: time.Since(start).Milliseconds()}
	hops := hopRe.FindAllStringSubmatch(out, -1)
	if len(hops) == 0 {
		r.Summary = "no route"
		if err != nil {
			r.Summary = firstLine(out, err.Error())
		}
		if strings.Contains(out, "not permitted") {
			r.Summary = "traceroute needs raw sockets: run the agent as root with NET_RAW (the compose files do)"
		}
		return r, nil
	}
	last := hops[len(hops)-1]
	r.Summary = fmt.Sprintf("%d hops", len(hops))
	if err != nil && err.Error() == "timed out" {
		r.Summary += " (stopped at the time limit)"
	}
	if last[2] == "*" {
		r.Summary += ", the last did not answer"
	} else {
		r.OK = true
		r.Summary += ", last " + last[2]
	}
	return r, nil
}

// Route shows how the server sends to target (ip route get); without a
// target, its addresses and routing table.
func Route(ctx context.Context, target string) (Result, error) {
	start := time.Now()
	if strings.TrimSpace(target) == "" {
		a, cutA, errA := run(ctx, "ip", "-br", "address")
		b, cutB, errB := run(ctx, "ip", "route")
		r := Result{Output: "# addresses\n" + a + "\n# routes\n" + b, Truncated: cutA || cutB, Millis: time.Since(start).Milliseconds()}
		r.OK = errA == nil && errB == nil
		r.Summary = fmt.Sprintf("%d interfaces, %d routes", lines(a), lines(b))
		return r, nil
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(target))
	if err != nil {
		return Result{}, bad("a route lookup needs an IP address")
	}
	out, cut, err := run(ctx, "ip", "route", "get", ip.String())
	r := Result{Output: out, Truncated: cut, Millis: time.Since(start).Milliseconds(), OK: err == nil}
	r.Summary = firstLine(out, "")
	if err != nil {
		r.Summary = firstLine(out, err.Error())
	}
	return r, nil
}

// DNS looks a name up (or an address, in reverse). kind: A, AAAA, CNAME, MX,
// TXT, NS or PTR ("" = A and AAAA). server: a DNS server to ask instead of the
// system's (an IP, port 53 unless given).
func DNS(ctx context.Context, name, kind, server string) (Result, error) {
	t, err := Target(name)
	if err != nil {
		return Result{}, err
	}
	res := net.DefaultResolver
	if server = strings.TrimSpace(server); server != "" {
		addr := server
		if _, _, err := net.SplitHostPort(server); err != nil {
			addr = net.JoinHostPort(server, "53")
		}
		host, _, _ := net.SplitHostPort(addr)
		if _, err := netip.ParseAddr(host); err != nil {
			return Result{}, bad("the DNS server must be an IP address")
		}
		res = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		}}
	}
	kind = strings.ToUpper(strings.TrimSpace(kind))
	if _, err := netip.ParseAddr(t); err == nil && (kind == "" || kind == "A" || kind == "AAAA") {
		kind = "PTR"
	}
	start := time.Now()
	var got []string
	switch kind {
	case "", "A", "AAAA":
		ips, e := res.LookupIP(ctx, map[string]string{"": "ip", "A": "ip4", "AAAA": "ip6"}[kind], t)
		err = e
		for _, ip := range ips {
			got = append(got, ip.String())
		}
	case "CNAME":
		var c string
		c, err = res.LookupCNAME(ctx, t)
		if c != "" {
			got = append(got, c)
		}
	case "MX":
		var mx []*net.MX
		mx, err = res.LookupMX(ctx, t)
		for _, m := range mx {
			got = append(got, fmt.Sprintf("%d %s", m.Pref, m.Host))
		}
	case "TXT":
		got, err = res.LookupTXT(ctx, t)
	case "NS":
		var ns []*net.NS
		ns, err = res.LookupNS(ctx, t)
		for _, n := range ns {
			got = append(got, n.Host)
		}
	case "PTR":
		got, err = res.LookupAddr(ctx, t)
	default:
		return Result{}, bad("record type: A, AAAA, CNAME, MX, TXT, NS or PTR")
	}
	if kind == "" {
		kind = "A/AAAA"
	}
	r := Result{Millis: time.Since(start).Milliseconds()}
	via := "the system's resolver"
	if server != "" {
		via = server
	}
	if err != nil {
		var de *net.DNSError
		msg := err.Error()
		if errors.As(err, &de) && de.IsNotFound {
			msg = "not found"
		}
		r.Summary = fmt.Sprintf("%s %s: %s (asked %s)", kind, t, msg, via)
		r.Output = r.Summary + "\n"
		return r, nil
	}
	r.OK = len(got) > 0
	r.Summary = fmt.Sprintf("%d %s record%s (asked %s, %d ms)", len(got), kind, plural(len(got)), via, r.Millis)
	r.Output = strings.Join(got, "\n") + "\n"
	return r, nil
}

// Ports parses "22, 80,443" or "8000-8005" into at most MaxPorts ports.
func Ports(s string) ([]int, error) {
	var out []int
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		lo, hi, isRange := strings.Cut(f, "-")
		a, err1 := strconv.Atoi(lo)
		b := a
		var err2 error
		if isRange {
			b, err2 = strconv.Atoi(hi)
		}
		if err1 != nil || err2 != nil || a < 1 || b > 65535 || b < a {
			return nil, bad("ports: numbers 1-65535, like 22, 80,443 or 8000-8005")
		}
		for p := a; p <= b; p++ {
			out = append(out, p)
			if len(out) > MaxPorts {
				return nil, bad("at most %d ports at once", MaxPorts)
			}
		}
	}
	if len(out) == 0 {
		return nil, bad("enter a port, like 443")
	}
	return out, nil
}

// TCP tries to connect to each port of target.
func TCP(ctx context.Context, target, ports string) (Result, error) {
	t, err := Target(target)
	if err != nil {
		return Result{}, err
	}
	ps, err := Ports(ports)
	if err != nil {
		return Result{}, err
	}
	start := time.Now()
	var b strings.Builder
	open := 0
	for _, p := range ps {
		d := net.Dialer{Timeout: 3 * time.Second}
		t0 := time.Now()
		c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(t, strconv.Itoa(p)))
		ms := time.Since(t0).Milliseconds()
		switch {
		case err == nil:
			open++
			fmt.Fprintf(&b, "%5d  open      %d ms  (%s)\n", p, ms, c.RemoteAddr())
			_ = c.Close()
		case isRefused(err):
			fmt.Fprintf(&b, "%5d  closed    %d ms  (refused)\n", p, ms)
		default:
			fmt.Fprintf(&b, "%5d  no answer %d ms  (%s)\n", p, ms, shortErr(err))
		}
	}
	r := Result{Output: b.String(), OK: open > 0, Millis: time.Since(start).Milliseconds()}
	r.Summary = fmt.Sprintf("%d of %d port%s open", open, len(ps), plural(len(ps)))
	return r, nil
}

func isRefused(err error) bool { return strings.Contains(err.Error(), "refused") }

func shortErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 {
		s = s[i+2:]
	}
	return s
}

// HTTP fetches a URL (GET, no redirects followed) and reports the timing,
// the status and the certificate.
func HTTP(ctx context.Context, raw string) (Result, error) {
	raw = strings.TrimSpace(raw)
	if raw != "" && !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Result{}, bad("a URL like https://example.com/")
	}
	if _, err := Target(u.Hostname()); err != nil {
		return Result{}, err
	}
	var dnsAt, connAt, tlsAt, firstByte time.Duration
	var t0 time.Time
	trace := &httptrace.ClientTrace{
		DNSDone:              func(httptrace.DNSDoneInfo) { dnsAt = time.Since(t0) },
		ConnectDone:          func(string, string, error) { connAt = time.Since(t0) },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { tlsAt = time.Since(t0) },
		GotFirstResponseByte: func() { firstByte = time.Since(t0) },
	}
	req, _ := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, u.String(), nil)
	req.Header.Set("User-Agent", "trapdoor-tools")
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // reported below, not trusted
	t0 = time.Now()
	resp, err := client.Do(req)
	r := Result{Millis: time.Since(t0).Milliseconds()}
	if err != nil {
		r.Summary = shortErr(err)
		r.Output = err.Error() + "\n"
		return r, nil
	}
	defer func() { _ = resp.Body.Close() }()
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", resp.Proto, resp.Status)
	for _, h := range []string{"Server", "Location", "Content-Type", "Strict-Transport-Security"} {
		if v := resp.Header.Get(h); v != "" {
			fmt.Fprintf(&b, "%s: %s\n", h, v)
		}
	}
	ms := func(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) + " ms" }
	fmt.Fprintf(&b, "\ntiming: DNS %s, connected %s", ms(dnsAt), ms(connAt))
	if tlsAt > 0 {
		fmt.Fprintf(&b, ", TLS %s", ms(tlsAt))
	}
	fmt.Fprintf(&b, ", first byte %s\n", ms(firstByte))
	certNote := ""
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		c := resp.TLS.PeerCertificates[0]
		days := int(time.Until(c.NotAfter).Hours() / 24)
		_, verr := c.Verify(certOpts(u.Hostname(), resp.TLS))
		trusted := "trusted"
		if verr != nil {
			trusted = "NOT trusted: " + shortErr(verr)
		}
		fmt.Fprintf(&b, "\ncertificate: %s, issued by %s\nvalid until %s (%d days), %s\n",
			c.Subject.CommonName, c.Issuer.CommonName, c.NotAfter.UTC().Format("2006-01-02"), days, trusted)
		certNote = fmt.Sprintf(", certificate %d days", days)
		if verr != nil {
			certNote = ", certificate not trusted"
		}
	}
	r.Output = b.String()
	r.OK = resp.StatusCode < 500
	r.Summary = fmt.Sprintf("%s in %d ms%s", resp.Status, firstByte.Milliseconds(), certNote)
	return r, nil
}

// certOpts checks the server's certificate against the system's roots, with
// the chain it sent.
func certOpts(host string, st *tls.ConnectionState) x509.VerifyOptions {
	o := x509.VerifyOptions{DNSName: host, Intermediates: x509.NewCertPool()}
	for _, c := range st.PeerCertificates[1:] {
		o.Intermediates.AddCert(c)
	}
	return o
}

func firstLine(s, fallback string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return fallback
}

func lines(s string) int {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
