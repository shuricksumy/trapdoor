package setup

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	ok := Request{PublicHost: "vpn.example.com"}
	Defaults(&ok)
	if err := Validate(ok); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	if ok.PublicPort != 1194 || ok.TLS != TLSCryptV2 || ok.Subnet != "10.8.0.0/24" {
		t.Errorf("defaults = %+v", ok)
	}
	for name, mut := range map[string]func(*Request){
		"no host":    func(r *Request) { r.PublicHost = "" },
		"bad host":   func(r *Request) { r.PublicHost = "vpn example" },
		"port":       func(r *Request) { r.PublicPort = 0; r.Port = 70000 },
		"proto":      func(r *Request) { r.Proto = "sctp" },
		"public net": func(r *Request) { r.Subnet = "8.8.8.0/24" },
		"subnet":     func(r *Request) { r.Subnet = "10.8.0.1/24" },
		"dns":        func(r *Request) { r.DNS = []string{"one.one.one.one"} },
		"key":        func(r *Request) { r.KeyType = "dsa" },
		"tls":        func(r *Request) { r.TLS = "magic" },
		"ca name":    func(r *Request) { r.CAName = "a/b" },
	} {
		r := ok
		mut(&r)
		if err := Validate(r); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	ip := ok
	ip.PublicHost = "203.0.113.7"
	if err := Validate(ip); err != nil {
		t.Errorf("IP host: %v", err)
	}
}

func TestRender(t *testing.T) {
	r := Request{PublicHost: "vpn.example.com", PublicPort: 443, Port: 1194, Proto: "tcp", DNS: []string{"10.8.0.1"},
		FullTunnel: true, TwoFactor: true, TLS: TLSCryptV2}
	Defaults(&r)
	p := Paths{CA: "easy-rsa/pki/ca.crt", Cert: "easy-rsa/pki/issued/server.crt", Key: "easy-rsa/pki/private/server.key",
		CRL: "crl.pem", TLSKey: "tls-crypt-v2.key", CCD: "ccd", Management: "127.0.0.1 2080", AuthVerify: "/usr/local/bin/trapdoor auth-verify", Group: "nogroup"}
	conf := ServerConf(r, p, time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC))
	for _, want := range []string{"port 1194\nproto tcp-server\n", "server 10.8.0.0 255.255.255.0", "tls-crypt-v2 tls-crypt-v2.key",
		"crl-verify crl.pem", "client-config-dir ccd", `push "redirect-gateway def1 bypass-dhcp"`, `push "dhcp-option DNS 10.8.0.1"`,
		`auth-user-pass-verify "/usr/local/bin/trapdoor auth-verify" via-file`, "management 127.0.0.1 2080", "group nogroup"} {
		if !strings.Contains(conf, want) {
			t.Errorf("server.conf missing %q:\n%s", want, conf)
		}
	}
	if strings.Contains(conf, "explicit-exit-notify") {
		t.Error("explicit-exit-notify is UDP only")
	}
	tmpl := ClientTemplate(r)
	for _, want := range []string{"remote vpn.example.com 443", "proto tcp", "auth-user-pass", "verify-x509-name server name"} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("client template missing %q:\n%s", want, tmpl)
		}
	}
}
