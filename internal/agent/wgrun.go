package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// wgRunner brings WireGuard interfaces up, applies configuration changes
// and reads their status. The configuration file is written before Apply.
type wgRunner interface {
	// Apply brings the interface up from conf, or applies the new peers to
	// a running one without disconnecting anybody. restart takes it down
	// and up again (needed when its address changes). subnet gets
	// forwarding and NAT; prev, when different, was the subnet before.
	Apply(ctx context.Context, id, conf string, subnet, prev netip.Prefix, restart bool) error
	// Down takes the interface down (conf is its last configuration).
	Down(ctx context.Context, id, conf string, subnet netip.Prefix) error
	// Dump returns `wg show <id> dump`.
	Dump(ctx context.Context, id string) (string, error)
}

// wgDirect runs wg and wg-quick itself (agent as root on the host).
// Forwarding and NAT are set up by the agent, not by wg-quick hooks:
// Ubuntu's AppArmor profile for wg-quick does not allow running them.
type wgDirect struct {
	toolsDir    string
	iptablesDir string
	ipForward   string // /proc/sys/net/ipv4/ip_forward
}

func (d wgDirect) bin(name string) string {
	if d.toolsDir != "" && (name == "wg" || name == "wg-quick") {
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

func (d wgDirect) Apply(ctx context.Context, id, conf string, subnet, prev netip.Prefix, restart bool) error {
	if d.up(ctx, id) && !restart {
		stripped, err := d.run(ctx, nil, "wg-quick", "strip", conf)
		if err != nil {
			return err
		}
		if _, err = d.run(ctx, stripped, "wg", "syncconf", id, "/dev/stdin"); err != nil {
			return err
		}
	} else {
		if d.up(ctx, id) {
			if _, err := d.run(ctx, nil, "wg-quick", "down", conf); err != nil {
				return err
			}
		}
		if _, err := d.run(ctx, nil, "wg-quick", "up", conf); err != nil {
			return err
		}
	}
	if prev.IsValid() && prev != subnet {
		d.route(ctx, id, prev, false)
	}
	return d.route(ctx, id, subnet, true)
}

func (d wgDirect) Down(ctx context.Context, id, conf string, subnet netip.Prefix) error {
	if !d.up(ctx, id) {
		return nil
	}
	if _, err := d.run(ctx, nil, "wg-quick", "down", conf); err != nil {
		return err
	}
	d.route(ctx, id, subnet, false)
	return nil
}

// route turns on IPv4 forwarding and adds (or removes) the rules that let
// the subnet out: FORWARD accept for the interface and NAT for anything
// from the subnet that does not go back into the tunnel.
func (d wgDirect) route(ctx context.Context, id string, subnet netip.Prefix, add bool) error {
	if add && d.ipForward != "" {
		if err := os.WriteFile(d.ipForward, []byte("1\n"), 0o644); err != nil {
			return fmt.Errorf("turn on IP forwarding: %w", err)
		}
	}
	ipt := "iptables"
	if d.iptablesDir != "" {
		ipt = filepath.Join(d.iptablesDir, "iptables")
	}
	rules := [][]string{
		{"FORWARD", "-i", id, "-j", "ACCEPT"},
		{"FORWARD", "-o", id, "-j", "ACCEPT"},
		{"-t", "nat", "POSTROUTING", "-s", subnet.String(), "!", "-o", id, "-j", "MASQUERADE"},
	}
	for _, r := range rules {
		table, chain, spec := []string{}, r[0], r[1:]
		if r[0] == "-t" {
			table, chain, spec = r[:2], r[2], r[3:]
		}
		args := func(op string) []string { return append(append(slices.Clone(table), op, chain), spec...) }
		_, err := d.run(ctx, nil, ipt, args("-C")...)
		switch {
		case add && err != nil:
			if _, err := d.run(ctx, nil, ipt, args("-I")...); err != nil {
				return err
			}
		case !add && err == nil:
			_, _ = d.run(ctx, nil, ipt, args("-D")...)
		}
	}
	return nil
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

func (f wgFile) Apply(context.Context, string, string, netip.Prefix, netip.Prefix, bool) error {
	return nil
}
func (f wgFile) Down(context.Context, string, string, netip.Prefix) error { return nil }

func (f wgFile) Dump(_ context.Context, id string) (string, error) {
	if b, err := os.ReadFile(wgPath(f.dir, id, ".error")); err == nil && len(bytes.TrimSpace(b)) > 0 {
		return "", errors.New(strings.TrimSpace(string(b)))
	}
	path := wgPath(f.dir, id, ".dump")
	fi, err := os.Stat(path)
	if err != nil || f.now().Sub(fi.ModTime()) > wgDumpMaxAge {
		return "", errors.New("the VPN container has not reported this WireGuard interface (is it running?)")
	}
	b, err := os.ReadFile(path)
	return string(b), err
}
