package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// wgRunner brings WireGuard interfaces up, applies configuration changes
// and reads their status. The configuration file is written before Apply.
type wgRunner interface {
	// Apply brings the interface up from conf, or applies the new peers to
	// a running one without disconnecting anybody. restart takes it down
	// and up again (needed when its address changes).
	Apply(ctx context.Context, id, conf string, restart bool) error
	// Down takes the interface down (conf is its last configuration).
	Down(ctx context.Context, id, conf string) error
	// Dump returns `wg show <id> dump`.
	Dump(ctx context.Context, id string) (string, error)
}

// wgDirect runs wg and wg-quick itself (agent as root on the host).
type wgDirect struct {
	toolsDir string
}

func (d wgDirect) bin(name string) string {
	if d.toolsDir != "" {
		return filepath.Join(d.toolsDir, name)
	}
	return name
}

func (d wgDirect) run(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, d.bin(name), args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return out, fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return out, nil
}

func (d wgDirect) up(ctx context.Context, id string) bool {
	_, err := d.run(ctx, nil, "wg", "show", id, "public-key")
	return err == nil
}

func (d wgDirect) Apply(ctx context.Context, id, conf string, restart bool) error {
	if d.up(ctx, id) {
		if !restart {
			stripped, err := d.run(ctx, nil, "wg-quick", "strip", conf)
			if err != nil {
				return err
			}
			_, err = d.run(ctx, stripped, "wg", "syncconf", id, "/dev/stdin")
			return err
		}
		if _, err := d.run(ctx, nil, "wg-quick", "down", conf); err != nil {
			return err
		}
	}
	_, err := d.run(ctx, nil, "wg-quick", "up", conf)
	return err
}

func (d wgDirect) Down(ctx context.Context, id, conf string) error {
	if !d.up(ctx, id) {
		return nil
	}
	_, err := d.run(ctx, nil, "wg-quick", "down", conf)
	return err
}

func (d wgDirect) Dump(ctx context.Context, id string) (string, error) {
	out, err := d.run(ctx, nil, "wg", "show", id, "dump")
	return string(out), err
}

// wgFile leaves running the interface to the VPN container (the one with
// NET_ADMIN): it picks up <id>.conf, and writes `wg show <id> dump` to
// <id>.dump every few seconds and problems to <id>.error.
type wgFile struct {
	dir string
	now func() time.Time
}

// wgDumpMaxAge: an older dump means the VPN container is not running.
const wgDumpMaxAge = time.Minute

func (f wgFile) Apply(context.Context, string, string, bool) error { return nil }
func (f wgFile) Down(context.Context, string, string) error         { return nil }

func (f wgFile) Dump(_ context.Context, id string) (string, error) {
	if b, err := os.ReadFile(filepath.Join(f.dir, id+".error")); err == nil && len(bytes.TrimSpace(b)) > 0 {
		return "", errors.New(strings.TrimSpace(string(b)))
	}
	path := filepath.Join(f.dir, id+".dump")
	fi, err := os.Stat(path)
	if err != nil || f.now().Sub(fi.ModTime()) > wgDumpMaxAge {
		return "", errors.New("the VPN container has not reported this WireGuard interface (is it running?)")
	}
	b, err := os.ReadFile(path)
	return string(b), err
}
