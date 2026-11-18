package serverconf

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Problem is one finding of Check. Errors block saving; warnings do not.
type Problem struct {
	Error   bool   `json:"error"`
	Message string `json:"message"`
}

// Check validates config text before it is written: the openvpn binary
// parses it (unknown options, bad values), referenced files must exist, and
// options tunnelward relies on are reported when missing. dir is the
// directory OpenVPN runs in (relative paths resolve against it).
func Check(ctx context.Context, raw, dir, openvpnBin string) []Problem {
	var out []Problem
	add := func(isErr bool, format string, a ...any) {
		out = append(out, Problem{Error: isErr, Message: fmt.Sprintf(format, a...)})
	}

	if strings.Contains(raw, "\x00") {
		return []Problem{{Error: true, Message: "config contains a NUL byte"}}
	}
	if len(raw) > 512<<10 {
		return []Problem{{Error: true, Message: "config is larger than 512 KiB"}}
	}

	// 1. OpenVPN's own parser: --version after --config parses every option
	//    and exits without starting anything.
	if openvpnBin != "" {
		if msg, err := parseWithOpenVPN(ctx, raw, dir, openvpnBin); err != nil {
			add(false, "could not run %s to check the config: %v", openvpnBin, err)
		} else if msg != "" {
			add(true, "%s", msg)
		}
	}

	// 2. Files and required options.
	have := map[string]bool{}
	inline := map[string]bool{}
	for _, l := range parseLines(raw) {
		t := strings.TrimSpace(l.text)
		if strings.HasPrefix(t, "<") && !strings.HasPrefix(t, "</") {
			inline[strings.Trim(t, "<>")] = true
		}
		if l.name == "" {
			continue
		}
		have[l.name] = true
		// Values OpenVPN only checks at startup.
		switch l.name {
		case "port":
			if n, err := strconv.Atoi(first(l.args)); err != nil || n < 1 || n > 65535 {
				add(true, "port %q must be a number 1-65535", first(l.args))
			}
		case "proto":
			if !slices.Contains([]string{"udp", "tcp", "udp4", "tcp4", "udp6", "tcp6", "tcp-server", "tcp4-server", "tcp6-server"}, first(l.args)) {
				add(true, "proto %q is not a server protocol (udp or tcp-server)", first(l.args))
			}
		case "server":
			if len(l.args) < 2 {
				add(true, `"server" needs a network and a netmask`)
			} else if _, err := toPrefix(l.args[0], l.args[1]); err != nil {
				add(true, "server: %v", err)
			}
		case "topology":
			if !slices.Contains([]string{"subnet", "net30", "p2p"}, first(l.args)) {
				add(true, "topology %q must be subnet, net30 or p2p", first(l.args))
			}
		}
		switch l.name {
		case "ca", "cert", "key", "dh", "crl-verify", "tls-crypt", "tls-auth", "tls-crypt-v2", "pkcs12", "secret":
			if len(l.args) == 0 || l.args[0] == "[inline]" || (l.name == "dh" && l.args[0] == "none") {
				continue
			}
			p := strings.Trim(l.args[0], `"`)
			if !filepath.IsAbs(p) {
				p = filepath.Join(dir, p)
			}
			if _, err := os.Stat(p); err != nil {
				add(true, "%s: file %s not found", l.name, l.args[0])
			}
		case "chroot", "cd", "tmp-dir":
			if len(l.args) > 0 {
				p := l.args[0]
				if !filepath.IsAbs(p) {
					p = filepath.Join(dir, p)
				}
				if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
					add(true, "%s: directory %s not found (OpenVPN will not start)", l.name, l.args[0])
				}
			}
		case "client-config-dir":
			if len(l.args) > 0 {
				p := l.args[0]
				if !filepath.IsAbs(p) {
					p = filepath.Join(dir, p)
				}
				if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
					add(true, "client-config-dir: directory %s not found (OpenVPN will not start)", l.args[0])
				}
			}
		}
	}
	for _, need := range []string{"ca", "cert", "key"} {
		if !have[need] && !inline[need] {
			add(true, "%q is missing", need)
		}
	}
	if !have["server"] && !have["mode"] {
		add(true, `"server" is missing: tunnelward manages a server with a VPN subnet`)
	}
	if !have["management"] {
		add(false, `no "management" line: live sessions, kicking and restarts from tunnelward will not work`)
	}
	if !have["crl-verify"] {
		add(false, `no "crl-verify" line: revoked clients can still connect`)
	}
	if !have["client-config-dir"] {
		add(false, `no "client-config-dir" line: per-client network settings are ignored`)
	}
	return out
}

func parseWithOpenVPN(ctx context.Context, raw, dir, bin string) (string, error) {
	f, err := os.CreateTemp("", "tw-server-*.conf")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.WriteString(raw); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--config", f.Name(), "--version")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil && ctx.Err() != nil {
		return "", ctx.Err()
	}
	if _, ok := err.(*exec.Error); ok {
		return "", err
	}
	for _, l := range strings.Split(string(out), "\n") {
		if i := strings.Index(l, "Options error:"); i >= 0 {
			msg := strings.TrimSpace(l[i+len("Options error:"):])
			// Point at the line in the user's file, not our temp file.
			msg = strings.ReplaceAll(msg, f.Name(), "server.conf")
			return msg, nil
		}
	}
	if !bytes.Contains(out, []byte("OpenVPN")) {
		return "", fmt.Errorf("unexpected output: %s", bytes.TrimSpace(out))
	}
	return "", nil
}

func first(a []string) string {
	if len(a) == 0 {
		return ""
	}
	return a[0]
}

// HasErrors reports whether any problem blocks saving.
func HasErrors(ps []Problem) bool {
	for _, p := range ps {
		if p.Error {
			return true
		}
	}
	return false
}
