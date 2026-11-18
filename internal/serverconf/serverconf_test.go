package serverconf

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Shaped like an openvpn-install / openvpn-ui server.conf.
const sample = `management 0.0.0.0 2080  # Don't change this line. OpenVPN UI uses this feature to manage the server
port 1194
proto udp
dev tun
user nobody
group nogroup
persist-key
persist-tun
keepalive 10 120
topology subnet
server 10.8.0.0 255.255.255.0
ifconfig-pool-persist ipp.txt
# DNS from the local resolver
push "dhcp-option DNS 1.1.1.1"
push "dhcp-option DNS 1.0.0.1"
push "redirect-gateway def1 bypass-dhcp"
route 192.168.50.0 255.255.255.0
dh none
tls-crypt-v2 tls-crypt-v2.key
crl-verify crl.pem
ca ca.crt
cert server.crt
key server.key
client-config-dir ccd
status /var/log/openvpn/status.log
verb 3
`

func TestParse(t *testing.T) {
	s := Parse(sample)
	if s.Port != 1194 || s.Proto != "udp" || s.Subnet != "10.8.0.0/24" || s.Topology != "subnet" ||
		!s.FullTunnel || s.ClientToClient || strings.Join(s.DNS, ",") != "1.1.1.1,1.0.0.1" ||
		strings.Join(s.Routes, ",") != "192.168.50.0/24" || len(s.PushRoutes) != 0 {
		t.Fatalf("settings = %+v", s)
	}
	if d := Parse("proto tcp-server\n"); d.Proto != "tcp" || d.Port != 1194 || d.Topology != "net30" {
		t.Errorf("defaults = %+v", d)
	}
}

func TestApplyPreserves(t *testing.T) {
	s := Parse(sample)
	s.Port = 443
	s.Proto = "tcp"
	s.DNS = []string{"10.8.0.1"}
	s.FullTunnel = false
	s.ClientToClient = true
	s.PushRoutes = []string{"192.168.1.0/24"}
	s.Routes = append(s.Routes, "192.168.60.0/24")
	out := Apply(sample, s)

	for _, want := range []string{
		"management 0.0.0.0 2080  # Don't change this line.",                      // untouched, comment kept
		"port 443\nproto tcp-server\ndev tun",                                     // replaced in place
		"# DNS from the local resolver\npush \"dhcp-option DNS 10.8.0.1\"\nroute", // set replaced at first position
		"route 192.168.50.0 255.255.255.0\nroute 192.168.60.0 255.255.255.0",
		"user nobody", "status /var/log/openvpn/status.log", "verb 3",
		"# Added by trapdoor\nclient-to-client\npush \"route 192.168.1.0 255.255.255.0\"",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, gone := range []string{"redirect-gateway", "DNS 1.1.1.1", "DNS 1.0.0.1", "port 1194"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q should be gone", gone)
		}
	}
	if Parse(out).Port != 443 || !Parse(out).ClientToClient {
		t.Error("round trip failed")
	}
	// Applying the parsed settings unchanged is a no-op for active lines.
	if again := Apply(out, Parse(out)); again != out {
		t.Errorf("not idempotent:\n%s\n---\n%s", out, again)
	}
}

func TestValidate(t *testing.T) {
	ok := Parse(sample)
	if err := Validate(ok); err != nil {
		t.Fatalf("sample invalid: %v", err)
	}
	for name, mut := range map[string]func(*Settings){
		"port":     func(s *Settings) { s.Port = 70000 },
		"proto":    func(s *Settings) { s.Proto = "sctp" },
		"subnet":   func(s *Settings) { s.Subnet = "10.8.0.5/24" },
		"tiny":     func(s *Settings) { s.Subnet = "10.8.0.0/30" },
		"dns":      func(s *Settings) { s.DNS = []string{"dns.google"} },
		"domain":   func(s *Settings) { s.Domain = "bad domain" },
		"overlap":  func(s *Settings) { s.Routes = []string{"10.8.0.0/16"} },
		"notacidr": func(s *Settings) { s.PushRoutes = []string{"192.168.1.1/24"} },
	} {
		s := ok
		mut(&s)
		if err := Validate(s); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestCheckWithOpenVPN(t *testing.T) {
	bin, err := exec.LookPath("openvpn")
	if err != nil {
		t.Skip("openvpn not installed")
	}
	dir := t.TempDir()
	for _, f := range []string{"ca.crt", "server.crt", "server.key", "crl.pem", "tls-crypt-v2.key"} {
		_ = os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600)
	}
	_ = os.Mkdir(filepath.Join(dir, "ccd"), 0o755)
	ctx := context.Background()

	if ps := Check(ctx, sample, dir, bin); HasErrors(ps) {
		t.Fatalf("sample has errors: %+v", ps)
	}
	ps := Check(ctx, sample+"bogus-option 1\n", dir, bin)
	if !HasErrors(ps) || !strings.Contains(ps[0].Message, "bogus-option") || !strings.Contains(ps[0].Message, "server.conf") {
		t.Errorf("unknown option: %+v", ps)
	}
	if ps := Check(ctx, strings.Replace(sample, "port 1194", "port lots", 1), dir, bin); !HasErrors(ps) {
		t.Errorf("bad port accepted: %+v", ps)
	}
	_ = os.Remove(filepath.Join(dir, "server.key"))
	if ps := Check(ctx, sample, dir, bin); !HasErrors(ps) || !strings.Contains(ps[0].Message, "server.key") {
		t.Errorf("missing key file: %+v", ps)
	}
	if ps := Check(ctx, sample+"chroot /nonexistent-dir\n", dir, bin); !HasErrors(ps) {
		t.Errorf("missing chroot dir accepted: %+v", ps)
	}
	noMgmt := strings.Replace(sample, "management 0.0.0.0 2080", "", 1)
	_ = os.WriteFile(filepath.Join(dir, "server.key"), []byte("x"), 0o600)
	ps = Check(ctx, noMgmt, dir, bin)
	if HasErrors(ps) || len(ps) != 1 || !strings.Contains(ps[0].Message, "management") {
		t.Errorf("no management: %+v", ps)
	}
}
