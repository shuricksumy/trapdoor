package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shuricksumy/trapdoor/internal/backup"
)

// makeBackup writes a backup like the web UI makes one.
func makeBackup(t *testing.T, files map[string]string, pass string) string {
	t.Helper()
	var buf bytes.Buffer
	var w io.Writer = &buf
	var enc io.WriteCloser
	if pass != "" {
		var err error
		if enc, err = backup.NewEncrypter(&buf, pass); err != nil {
			t.Fatal(err)
		}
		w = enc
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	m, _ := json.Marshal(backup.Manifest{Format: 1, Version: "test", Created: time.Now(), Hostname: "vpn1", Parts: []string{"agent", "web", "hosts/vps"}})
	add := func(name, content string) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(content))
	}
	add("manifest.json", string(m))
	for name, content := range files {
		add(name, content)
	}
	_ = tw.Close()
	_ = gz.Close()
	if enc != nil {
		_ = enc.Close()
	}
	p := filepath.Join(t.TempDir(), "b.tdbackup")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRestore(t *testing.T) {
	root := t.TempDir()
	ovpn, wg, db := filepath.Join(root, "openvpn"), filepath.Join(root, "wireguard"), filepath.Join(root, "data", "trapdoor.db")
	t.Setenv("TD_OPENVPN_DIR", ovpn)
	t.Setenv("TD_WIREGUARD_DIR", wg)
	t.Setenv("TD_AMNEZIAWG_DIR", filepath.Join(root, "amneziawg"))
	t.Setenv("TD_DB_PATH", db)
	t.Setenv("TD_BACKUP_PASSPHRASE", "pass phrase")
	file := makeBackup(t, map[string]string{
		"agent/openvpn/server.conf":             "port 1194\n",
		"agent/openvpn/easy-rsa/pki/ca.crt":     "CA",
		"agent/wireguard/wg0.json":              "{}",
		"hosts/vps/openvpn/server.conf":         "port 443\n",
		"hosts/vps/openvpn/easy-rsa/pki/ca.crt": "VPS CA",
		"web/trapdoor.db":                       "DB",
	}, "pass phrase")
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }

	if err := runRestore([]string{"agent", file}); err != nil {
		t.Fatal(err)
	}
	if read(filepath.Join(ovpn, "server.conf")) != "port 1194\n" || read(filepath.Join(ovpn, "easy-rsa/pki/ca.crt")) != "CA" || read(filepath.Join(wg, "wg0.json")) != "{}" {
		t.Error("agent part not restored")
	}
	// A set-up machine is not overwritten without --force.
	if err := runRestore([]string{"agent", file}); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("second restore: %v", err)
	}
	if err := runRestore([]string{"agent", "--host", "vps", "--force", file}); err != nil {
		t.Fatal(err)
	}
	if read(filepath.Join(ovpn, "server.conf")) != "port 443\n" || read(filepath.Join(ovpn, "easy-rsa/pki/ca.crt")) != "VPS CA" {
		t.Error("host part not restored")
	}
	if err := runRestore([]string{"agent", "--host", "nowhere", "--force", file}); err == nil {
		t.Error("unknown host restored")
	}

	if err := runRestore([]string{"web", file}); err != nil {
		t.Fatal(err)
	}
	if read(db) != "DB" {
		t.Error("database not restored")
	}
	if err := runRestore([]string{"web", file}); err == nil {
		t.Error("database replaced without --force")
	}
	if err := runRestore([]string{"web", "--force", file}); err != nil {
		t.Fatal(err)
	}
	if old, _ := filepath.Glob(db + ".before-restore-*"); len(old) != 1 {
		t.Errorf("old database not kept: %v", old)
	}

	t.Setenv("TD_BACKUP_PASSPHRASE", "wrong")
	if err := runRestore([]string{"web", "--force", file}); err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Errorf("wrong passphrase: %v", err)
	}
	for _, bad := range [][]string{{}, {"all", file}, {"web", "--host", "vps", file}, {"agent"}} {
		if err := runRestore(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
