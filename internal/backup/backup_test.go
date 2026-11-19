package backup

import (
	"archive/tar"
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func encrypt(t *testing.T, data []byte, pass string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewEncrypter(&buf, pass)
	if err != nil {
		t.Fatal(err)
	}
	// Odd write sizes cross chunk borders.
	for len(data) > 0 {
		k := min(len(data), 10_007)
		if _, err := w.Write(data[:k]); err != nil {
			t.Fatal(err)
		}
		data = data[k:]
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decrypt(enc []byte, pass string) ([]byte, error) {
	r, err := NewDecrypter(bytes.NewReader(enc), pass)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func TestEncryptRoundTrip(t *testing.T) {
	for _, size := range []int{0, 1, chunkSize - 1, chunkSize, chunkSize + 1, 3*chunkSize + 12345} {
		data := make([]byte, size)
		_, _ = rand.Read(data)
		enc := encrypt(t, data, "correct horse")
		if !Encrypted(enc) {
			t.Fatal("no header")
		}
		got, err := decrypt(enc, "correct horse")
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("size %d: %v (got %d bytes)", size, err, len(got))
		}
		if _, err := decrypt(enc, "wrong"); !errors.Is(err, ErrPassphrase) {
			t.Errorf("size %d, wrong passphrase: %v", size, err)
		}
	}
}

func TestEncryptDetectsChanges(t *testing.T) {
	data := make([]byte, 2*chunkSize+100)
	enc := encrypt(t, data, "pw")
	firstChunkEnd := headerLen + 4 + chunkSize + 16
	for name, bad := range map[string][]byte{
		"truncated":        enc[:firstChunkEnd],
		"flipped bit":      func() []byte { b := bytes.Clone(enc); b[headerLen+20] ^= 1; return b }(),
		"changed header":   func() []byte { b := bytes.Clone(enc); b[len(magic)+6] ^= 1; return b }(),
		"data after end":   append(bytes.Clone(enc), 0, 0, 0, 1, 0),
		"chunks reordered": append(append(bytes.Clone(enc[:headerLen]), enc[firstChunkEnd:2*firstChunkEnd-headerLen]...), enc[headerLen:firstChunkEnd]...),
	} {
		if _, err := decrypt(bad, "pw"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := NewEncrypter(io.Discard, ""); err == nil {
		t.Error("empty passphrase accepted")
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAddDirExtract(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "server.conf"), "port 1194\n")
	write(t, filepath.Join(src, "pki/private/ca.key"), "KEY")
	write(t, filepath.Join(src, "ccd/alice"), "#2FA_KEY:00\n")
	write(t, filepath.Join(src, "wg0.dump"), "state")
	write(t, filepath.Join(src, "servers/x/server.conf"), "port 443\n")
	if err := os.Symlink("server.conf", filepath.Join(src, "link.conf")); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := AddDir(tw, src, "agent/openvpn", "servers"); err != nil {
		t.Fatal(err)
	}
	if err := AddDir(tw, filepath.Join(src, "servers"), "agent/openvpn-servers"); err != nil {
		t.Fatal(err)
	}
	if err := AddDir(tw, filepath.Join(src, "missing"), "agent/none"); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	dst, servers := t.TempDir(), t.TempDir()
	targets := []Target{{"agent/openvpn", dst}, {"agent/openvpn-servers", servers}}
	n, err := Extract(bytes.NewReader(buf.Bytes()), targets, false)
	if err != nil || n != 4 {
		t.Fatalf("extract: %d files, %v", n, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "pki/private/ca.key")); string(b) != "KEY" {
		t.Error("ca.key not restored")
	}
	if fi, _ := os.Stat(filepath.Join(dst, "pki/private/ca.key")); fi.Mode().Perm() != 0o600 {
		t.Errorf("ca.key mode %v", fi.Mode().Perm())
	}
	if l, _ := os.Readlink(filepath.Join(dst, "link.conf")); l != "server.conf" {
		t.Errorf("link = %q", l)
	}
	if _, err := os.Stat(filepath.Join(dst, "wg0.dump")); err == nil {
		t.Error("runtime state backed up")
	}
	if _, err := os.Stat(filepath.Join(dst, "servers")); err == nil {
		t.Error("excluded directory backed up")
	}
	if b, _ := os.ReadFile(filepath.Join(servers, "x/server.conf")); string(b) != "port 443\n" {
		t.Error("second prefix not restored")
	}

	// Existing files stop a restore unless forced.
	if _, err := Extract(bytes.NewReader(buf.Bytes()), targets, false); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Errorf("restore over existing files: %v", err)
	}
	write(t, filepath.Join(dst, "server.conf"), "changed")
	if _, err := Extract(bytes.NewReader(buf.Bytes()), targets, true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "server.conf")); string(b) != "port 1194\n" {
		t.Error("forced restore did not replace the file")
	}
}

func TestExtractRefusesEscapes(t *testing.T) {
	for name, h := range map[string]*tar.Header{
		"dot dot":         {Name: "agent/openvpn/../../etc/passwd", Typeflag: tar.TypeReg, Mode: 0o644},
		"link out":        {Name: "agent/openvpn/l", Typeflag: tar.TypeSymlink, Linkname: "../../etc"},
		"link absolute":   {Name: "agent/openvpn/l", Typeflag: tar.TypeSymlink, Linkname: "/etc"},
		"link to sibling": {Name: "agent/openvpn/l", Typeflag: tar.TypeSymlink, Linkname: "../wireguard"},
		"device":          {Name: "agent/openvpn/dev", Typeflag: tar.TypeChar, Mode: 0o644},
	} {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		_ = tw.Close()
		dir := t.TempDir()
		if _, err := Extract(&buf, []Target{{"agent/openvpn", filepath.Join(dir, "openvpn")}}, true); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if _, err := os.Lstat(filepath.Join(dir, "etc")); err == nil {
			t.Errorf("%s: wrote outside", name)
		}
	}
}
