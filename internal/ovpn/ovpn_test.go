package ovpn

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shuricksumy/tunnelward/internal/pki"
)

const template = `client
proto udp
explicit-exit-notify
remote vpn.example.com 1194
dev tun
nobind
remote-cert-tls server
verb 3`

var bundle = pki.Bundle{
	CA:   []byte("-----BEGIN CERTIFICATE-----\nCA\n-----END CERTIFICATE-----\n"),
	Cert: []byte("-----BEGIN CERTIFICATE-----\nCERT\n-----END CERTIFICATE-----\n"),
	Key:  []byte("-----BEGIN PRIVATE KEY-----\nKEY\n-----END PRIVATE KEY-----"),
}

func setup(t *testing.T, serverConf string, files map[string]string) *Generator {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(templateTxt, template)
	write(serverConfFile, serverConf)
	for n, c := range files {
		write(n, c)
	}
	return &Generator{Dir: dir}
}

const serverConfFile = "server.conf"

func TestDetectTLS(t *testing.T) {
	cases := []struct {
		conf string
		mode TLSMode
		path string
	}{
		{"port 1194\n", TLSNone, ""},
		{"# tls-crypt old.key\ntls-crypt tls-crypt.key\n", TLSCrypt, "/etc/openvpn/tls-crypt.key"},
		{"; tls-auth x 0\ntls-auth /keys/ta.key 0\n", TLSAuth, "/keys/ta.key"},
		{"tls-crypt-v2 tls-crypt-v2.key\n", TLSCryptV2, "/etc/openvpn/tls-crypt-v2.key"},
		{"  tls-crypt \"quoted.key\"\n", TLSCrypt, "/etc/openvpn/quoted.key"},
	}
	for _, c := range cases {
		got, err := DetectTLS([]byte(c.conf), "/etc/openvpn")
		if err != nil || got.Mode != c.mode || got.KeyPath != c.path {
			t.Errorf("%q: got %+v, %v; want %s %s", c.conf, got, err, c.mode, c.path)
		}
	}

	got, err := DetectTLS([]byte("port 1\n<tls-crypt>\n-----BEGIN OpenVPN Static key V1-----\nabc\n-----END OpenVPN Static key V1-----\n</tls-crypt>\n"), "/x")
	if err != nil || got.Mode != TLSCrypt || !strings.Contains(string(got.Inline), "abc") {
		t.Errorf("inline: %+v, %v", got, err)
	}
	for _, bad := range []string{"tls-crypt\n", "<tls-auth>\nabc\n"} {
		if _, err := DetectTLS([]byte(bad), "/x"); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestBuildTLSCrypt(t *testing.T) {
	g := setup(t, "port 1194\ntls-crypt tls-crypt.key\n", map[string]string{"tls-crypt.key": "STATIC-KEY\n"})
	out, err := g.Build(context.Background(), "alice", bundle)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.HasPrefix(s, template+"\n\n<ca>\n") {
		t.Errorf("template not at start:\n%s", s)
	}
	for _, want := range []string{
		"<ca>\n-----BEGIN CERTIFICATE-----\nCA\n-----END CERTIFICATE-----\n</ca>\n",
		"<cert>\n-----BEGIN CERTIFICATE-----\nCERT\n-----END CERTIFICATE-----\n</cert>\n",
		"<key>\n-----BEGIN PRIVATE KEY-----\nKEY\n-----END PRIVATE KEY-----\n</key>\n",
		"<tls-crypt>\nSTATIC-KEY\n</tls-crypt>\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "key-direction") {
		t.Error("tls-crypt must not set key-direction")
	}
}

func TestBuildTLSAuthAndNone(t *testing.T) {
	g := setup(t, "tls-auth ta.key 0\n", map[string]string{"ta.key": "TA"})
	out, err := g.Build(context.Background(), "bob", bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "key-direction 1\n<tls-auth>\nTA\n</tls-auth>\n") {
		t.Errorf("tls-auth block wrong:\n%s", out)
	}

	g = setup(t, "port 1194\n", nil)
	out, err = g.Build(context.Background(), "bob", bundle)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "<tls-") {
		t.Errorf("unexpected tls block:\n%s", out)
	}
}

func TestBuildErrors(t *testing.T) {
	g := setup(t, "tls-crypt missing.key\n", nil)
	if _, err := g.Build(context.Background(), "alice", bundle); err == nil {
		t.Error("missing key file: expected error")
	}
	if _, err := g.Build(context.Background(), "../alice", bundle); !errors.Is(err, pki.ErrInvalidName) {
		t.Errorf("bad name: err = %v", err)
	}
	if err := os.Remove(filepath.Join(g.Dir, templateTxt)); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Build(context.Background(), "alice", bundle); !errors.Is(err, ErrNoTemplate) {
		t.Errorf("no template: err = %v", err)
	}
	if _, err := (&Generator{Dir: t.TempDir()}).Build(context.Background(), "a", bundle); !errors.Is(err, ErrNoTemplate) {
		t.Errorf("empty dir: err = %v", err)
	}
}

func TestBuildTLSCryptV2(t *testing.T) {
	bin, err := exec.LookPath("openvpn")
	if err != nil {
		t.Skip("openvpn not installed")
	}
	g := setup(t, "tls-crypt-v2 tls-crypt-v2.key\n", nil)
	g.OpenVPNBin = bin
	serverKey := filepath.Join(g.Dir, "tls-crypt-v2.key")
	if out, err := exec.Command(bin, "--genkey", "tls-crypt-v2-server", serverKey).CombinedOutput(); err != nil {
		t.Fatalf("server key: %v %s", err, out)
	}

	first, err := g.Build(context.Background(), "alice", bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), "<tls-crypt-v2>\n-----BEGIN OpenVPN tls-crypt-v2 client key-----") {
		t.Fatalf("no v2 client key:\n%s", first)
	}
	keyPath := filepath.Join(g.Dir, keysV2Dir, "alice.key")
	fi, err := os.Stat(keyPath)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("client key file: %v %v", fi, err)
	}
	second, _ := g.Build(context.Background(), "alice", bundle)
	if string(first) != string(second) {
		t.Error("existing client key must be reused")
	}
	other, _ := g.Build(context.Background(), "bob", bundle)
	if string(other) == string(first) {
		t.Error("each client needs its own key")
	}

	if err := g.RemoveClient("alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(keyPath); !errors.Is(err, os.ErrNotExist) {
		t.Error("client key not removed")
	}
	if err := g.RemoveClient("alice"); err != nil {
		t.Errorf("second remove: %v", err)
	}
}
