package nettools

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestTarget(t *testing.T) {
	for in, want := range map[string]string{
		"10.8.0.1": "10.8.0.1", " vpn.example.com ": "vpn.example.com", "home-router.lan.": "home-router.lan",
		"::1": "::1", "fe80::1": "fe80::1", "localhost": "localhost",
	} {
		if got, err := Target(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	// nothing that could be an option, a command or a URL
	for _, in := range []string{"", "-c 100 1.1.1.1", "--help", "1.1.1.1; reboot", "$(id)", "a b", "host/path", "-", "a..b", strings.Repeat("a", 64) + ".com"} {
		if _, err := Target(in); !errors.Is(err, ErrBadInput) {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestPorts(t *testing.T) {
	p, err := Ports("22, 80,443 8000-8002")
	if err != nil || len(p) != 6 || p[5] != 8002 {
		t.Fatalf("%v %v", p, err)
	}
	for _, in := range []string{"", "0", "70000", "80-70", "x", "1-100"} {
		if _, err := Ports(in); !errors.Is(err, ErrBadInput) {
			t.Errorf("%q accepted", in)
		}
	}
}

// fakeTools puts scripts named ping / traceroute / ip first on PATH; each
// records its arguments and prints busybox-like output.
func fakeTools(t *testing.T) string {
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	scripts := map[string]string{
		"ping": `echo "PING $5 ($5): 56 data bytes"
echo "64 bytes from $5: seq=0 ttl=64 time=11.2 ms"
echo "64 bytes from $5: seq=1 ttl=64 time=13.4 ms"
echo ""
echo "--- $5 ping statistics ---"
echo "$2 packets transmitted, 2 packets received, 50% packet loss"
echo "round-trip min/avg/max = 11.2/12.3/13.4 ms"`,
		"traceroute": `echo "traceroute to $9 ($9), 20 hops max, 46 byte packets"
echo " 1  10.8.0.1  0.4 ms"
echo " 2  192.168.1.1  14.2 ms"
echo " 3  *"`,
		"ip": `if [ "$1" = route ] && [ "$2" = get ]; then echo "$3 dev tun0 src 10.8.0.1 uid 0"; echo "    cache"; else echo "tun0 UP 10.8.0.1/24"; fi`,
	}
	for name, body := range scripts {
		script := "#!/bin/sh\necho \"" + name + " $*\" >>" + args + "\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return args
}

func TestPingTracerouteRoute(t *testing.T) {
	args := fakeTools(t)
	ctx := context.Background()
	r, err := Ping(ctx, "10.8.0.6", 50) // capped at 10
	if err != nil || !r.OK || r.Summary != "2/10 replies, min 11.2 / avg 12.3 / max 13.4 ms, 80% loss" {
		t.Errorf("ping: %+v %v", r, err)
	}
	r, err = Traceroute(ctx, "192.168.1.20")
	if err != nil || r.OK || r.Summary != "3 hops, the last did not answer" {
		t.Errorf("traceroute: %+v %v", r, err)
	}
	r, err = Route(ctx, "192.168.1.20")
	if err != nil || !r.OK || r.Summary != "192.168.1.20 dev tun0 src 10.8.0.1 uid 0" {
		t.Errorf("route: %+v %v", r, err)
	}
	r, _ = Route(ctx, "")
	if !strings.Contains(r.Output, "# routes") || !r.OK {
		t.Errorf("route table: %+v", r)
	}
	if _, err := Route(ctx, "example.com"); !errors.Is(err, ErrBadInput) {
		t.Errorf("route by name: %v", err)
	}
	b, _ := os.ReadFile(args)
	want := "ping -c 10 -W 2 10.8.0.6\ntraceroute -n -q 1 -w 1 -m 20 192.168.1.20\nip route get 192.168.1.20\nip -br address\nip route\n"
	if string(b) != want {
		t.Errorf("commands run:\n%s\nwant\n%s", b, want)
	}
	// a bad target never reaches the program
	if _, err := Ping(ctx, "-f 10.8.0.6", 1); !errors.Is(err, ErrBadInput) {
		t.Errorf("option as target: %v", err)
	}
	if b2, _ := os.ReadFile(args); string(b2) != want {
		t.Error("a refused target ran something")
	}
}

func TestTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	open := ln.Addr().(*net.TCPAddr).Port
	closed := open + 1
	if c, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(closed)); err == nil {
		_ = c.Close() // free now: nothing listens there
	}
	r, err := TCP(context.Background(), "127.0.0.1", strconv.Itoa(open)+","+strconv.Itoa(closed))
	if err != nil || !r.OK || r.Summary != "1 of 2 ports open" || !strings.Contains(r.Output, "open") || !strings.Contains(r.Output, "closed") {
		t.Errorf("tcp: %+v %v", r, err)
	}
}

func TestHTTP(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new", http.StatusMovedPermanently)
			return
		}
		w.Header().Set("Server", "demo")
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()
	r, err := HTTP(context.Background(), srv.URL+"/")
	if err != nil || !r.OK || !strings.HasPrefix(r.Summary, "200 OK in ") || !strings.Contains(r.Summary, "certificate not trusted") ||
		!strings.Contains(r.Output, "Server: demo") || !strings.Contains(r.Output, "timing: DNS") {
		t.Errorf("https: %+v %v", r, err)
	}
	// redirects are reported, not followed
	r, _ = HTTP(context.Background(), srv.URL+"/old")
	if !strings.HasPrefix(r.Summary, "301") || !strings.Contains(r.Output, "Location: /new") {
		t.Errorf("redirect: %+v", r)
	}
	for _, in := range []string{"ftp://example.com", "https://", "https://-x/", "file:///etc/passwd"} {
		if _, err := HTTP(context.Background(), in); !errors.Is(err, ErrBadInput) {
			t.Errorf("%q accepted", in)
		}
	}
	// no scheme: https
	if r, _ := HTTP(context.Background(), "127.0.0.1:1"); r.OK || r.Output == "" {
		t.Errorf("unreachable: %+v", r)
	}
}

func TestDNS(t *testing.T) {
	ctx := context.Background()
	r, err := DNS(ctx, "localhost", "A", "")
	if err != nil || !r.OK || !strings.Contains(r.Output, "127.0.0.1") {
		t.Errorf("localhost: %+v %v", r, err)
	}
	if _, err := DNS(ctx, "example.com", "SOA", ""); !errors.Is(err, ErrBadInput) {
		t.Errorf("record type: %v", err)
	}
	if _, err := DNS(ctx, "example.com", "A", "dns.example.com"); !errors.Is(err, ErrBadInput) {
		t.Errorf("server by name: %v", err)
	}
	// a server that does not answer: an error line, not a failure of the tool
	ctx2, cancel := context.WithTimeout(ctx, 2e9)
	defer cancel()
	if r, err := DNS(ctx2, "example.com", "A", "127.0.0.1:1"); err != nil || r.OK || !strings.Contains(r.Summary, "asked 127.0.0.1:1") {
		t.Errorf("no answer: %+v %v", r, err)
	}
}
