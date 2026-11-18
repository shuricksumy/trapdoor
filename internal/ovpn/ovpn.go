// Package ovpn builds client .ovpn profiles.
//
// It follows what openvpn-ui / openvpn-install did, so existing servers keep
// producing the same files: the profile is <openvpn dir>/client-template.txt
// followed by inline <ca>, <cert>, <key> and the control-channel key that
// matches server.conf (tls-crypt, tls-auth or tls-crypt-v2).
package ovpn

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/shuricksumy/trapdoor/internal/pki"
)

// TLSMode is the control-channel protection configured on the server.
type TLSMode string

const (
	TLSNone     TLSMode = "none"
	TLSAuth     TLSMode = "tls-auth"
	TLSCrypt    TLSMode = "tls-crypt"
	TLSCryptV2  TLSMode = "tls-crypt-v2"
	templateTxt         = "client-template.txt"
	serverConf          = "server.conf"
	keysV2Dir           = "keys-v2"
)

// Errors; check with errors.Is.
var (
	ErrNoTemplate   = errors.New("client template not found")
	ErrNoServerConf = errors.New("server.conf not found")
)

// ServerTLS describes the server's control-channel key.
type ServerTLS struct {
	Mode    TLSMode
	KeyPath string // absolute path, when the key is a file
	Inline  []byte // key content, when inlined in server.conf
}

// Key returns the key content (inline or read from KeyPath).
func (t ServerTLS) Key() ([]byte, error) {
	if t.Inline != nil {
		return t.Inline, nil
	}
	return os.ReadFile(t.KeyPath)
}

// DetectTLS finds the tls-auth / tls-crypt / tls-crypt-v2 directive in a
// server config. Relative key paths are resolved against baseDir.
func DetectTLS(conf []byte, baseDir string) (ServerTLS, error) {
	sc := bufio.NewScanner(bytes.NewReader(conf))
	var (
		inBlock TLSMode
		buf     bytes.Buffer
	)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if inBlock != "" {
			if line == "</"+string(inBlock)+">" {
				return ServerTLS{Mode: inBlock, Inline: buf.Bytes()}, nil
			}
			buf.WriteString(line + "\n")
			continue
		}
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		for _, m := range []TLSMode{TLSCryptV2, TLSCrypt, TLSAuth} {
			if line == "<"+string(m)+">" {
				inBlock = m
				break
			}
		}
		if inBlock != "" {
			continue
		}
		f := strings.Fields(line)
		mode := TLSMode(f[0])
		if mode != TLSCryptV2 && mode != TLSCrypt && mode != TLSAuth {
			continue
		}
		if len(f) < 2 {
			return ServerTLS{}, fmt.Errorf("%s without key file", mode)
		}
		path := strings.Trim(f[1], `"`)
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, path)
		}
		return ServerTLS{Mode: mode, KeyPath: path}, nil
	}
	if err := sc.Err(); err != nil {
		return ServerTLS{}, err
	}
	if inBlock != "" {
		return ServerTLS{}, fmt.Errorf("unterminated <%s> block", inBlock)
	}
	return ServerTLS{Mode: TLSNone}, nil
}

// Generator builds profiles for one OpenVPN server directory.
type Generator struct {
	// Dir is the OpenVPN directory with client-template.txt and server.conf.
	Dir string
	// OpenVPNBin is the openvpn binary, used only to create tls-crypt-v2
	// client keys. Default "openvpn".
	OpenVPNBin string

	mu sync.Mutex // serializes tls-crypt-v2 key creation
}

// Build returns the .ovpn profile for name.
func (g *Generator) Build(ctx context.Context, name string, b pki.Bundle) ([]byte, error) {
	if !pki.ValidName(name) {
		return nil, fmt.Errorf("%w: %q", pki.ErrInvalidName, name)
	}
	tmpl, err := os.ReadFile(filepath.Join(g.Dir, templateTxt))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNoTemplate, filepath.Join(g.Dir, templateTxt))
	} else if err != nil {
		return nil, err
	}
	conf, err := os.ReadFile(filepath.Join(g.Dir, serverConf))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNoServerConf, filepath.Join(g.Dir, serverConf))
	} else if err != nil {
		return nil, err
	}
	tls, err := DetectTLS(conf, g.Dir)
	if err != nil {
		return nil, fmt.Errorf("server.conf: %w", err)
	}

	var out bytes.Buffer
	out.Write(withNewline(tmpl))
	out.WriteString("\n")
	block(&out, "ca", b.CA)
	block(&out, "cert", b.Cert)
	block(&out, "key", b.Key)

	switch tls.Mode {
	case TLSCrypt:
		key, err := tls.Key()
		if err != nil {
			return nil, fmt.Errorf("read tls-crypt key: %w", err)
		}
		block(&out, "tls-crypt", key)
	case TLSAuth:
		key, err := tls.Key()
		if err != nil {
			return nil, fmt.Errorf("read tls-auth key: %w", err)
		}
		out.WriteString("key-direction 1\n")
		block(&out, "tls-auth", key)
	case TLSCryptV2:
		key, err := g.clientV2Key(ctx, name, tls)
		if err != nil {
			return nil, err
		}
		block(&out, "tls-crypt-v2", key)
	}
	return out.Bytes(), nil
}

// RemoveClient deletes per-client files (the tls-crypt-v2 client key).
func (g *Generator) RemoveClient(name string) error {
	if !pki.ValidName(name) {
		return fmt.Errorf("%w: %q", pki.ErrInvalidName, name)
	}
	err := os.Remove(filepath.Join(g.Dir, keysV2Dir, name+".key"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// clientV2Key returns keys-v2/<name>.key, creating it with openvpn if needed.
// Existing keys (e.g. from openvpn-ui) are reused so profiles stay stable.
func (g *Generator) clientV2Key(ctx context.Context, name string, tls ServerTLS) ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	path := filepath.Join(g.Dir, keysV2Dir, name+".key")
	if key, err := os.ReadFile(path); err == nil {
		return key, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if tls.KeyPath == "" {
		return nil, errors.New("tls-crypt-v2 server key must be a file to create client keys")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}

	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	defer func() { _ = os.Remove(tmp) }()
	bin := g.OpenVPNBin
	if bin == "" {
		bin = "openvpn"
	}
	// Arguments only, no shell: name is validated and never interpreted.
	cmd := exec.CommandContext(ctx, bin, "--tls-crypt-v2", tls.KeyPath, "--genkey", "tls-crypt-v2-client", tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("openvpn --genkey tls-crypt-v2-client: %w: %s", err, bytes.TrimSpace(out))
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func block(w *bytes.Buffer, tag string, content []byte) {
	fmt.Fprintf(w, "<%s>\n", tag)
	w.Write(withNewline(bytes.TrimSpace(content)))
	fmt.Fprintf(w, "</%s>\n", tag)
}

func withNewline(b []byte) []byte {
	if len(b) == 0 || b[len(b)-1] == '\n' {
		return b
	}
	return append(b, '\n')
}
