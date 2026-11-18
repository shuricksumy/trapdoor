package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/shuricksumy/trapdoor/internal/firewall"
)

// fwTarget applies one rendered chain of access rules: directly with
// iptables, or through a rules file that the VPN container applies and
// answers in a status file ("ok <hash>" / "error <hash> <message>").
// OpenVPN has one (chain TD-FWD for tun+), each WireGuard server another
// (chain TD-<id> for its interface).
type fwTarget struct {
	mode        string // FirewallDirect | FirewallFile | FirewallOff
	chain       string
	iface       string // -i match of the INPUT and FORWARD jumps
	rulesPath   string
	statusPath  string
	iptablesDir string
	wait        time.Duration // file mode: how long to wait for the container

	applied string // direct mode: hash of the rules in the kernel
	lastErr string
}

const fwContainer = "the VPN container"

// apply makes rules active. It returns whether they are, and why not.
// In file mode wait says whether to wait for the container's answer.
func (t *fwTarget) apply(ctx context.Context, rules string, wait bool) (bool, string, error) {
	if err := writeAtomic(t.rulesPath, []byte(rules), 0o644); err != nil {
		return false, "", err
	}
	hash := firewall.Hash(rules)
	switch t.mode {
	case FirewallDirect:
		if err := t.iptablesApply(ctx, rules); err != nil {
			t.applied, t.lastErr = "", err.Error()
		} else {
			t.applied, t.lastErr = hash, ""
		}
		return t.applied == hash, t.lastErr, nil
	case FirewallFile:
		if !wait {
			return false, "", nil
		}
		ok, msg := t.waitForFile(ctx, hash)
		return ok, msg, nil
	}
	return false, "", nil
}

// state reports, without applying, whether rules are the active ones.
func (t *fwTarget) state(rules string) (bool, string) {
	hash := firewall.Hash(rules)
	switch t.mode {
	case FirewallDirect:
		return t.applied == hash, t.lastErr
	case FirewallFile:
		ok, known, msg := t.fileStatus(hash)
		if !known {
			return false, fwContainer + " has not applied the current rules yet"
		}
		return ok, msg
	}
	return false, ""
}

func (t *fwTarget) fileStatus(hash string) (applied, known bool, msg string) {
	data, err := os.ReadFile(t.statusPath)
	if err != nil {
		return false, false, ""
	}
	f := strings.SplitN(strings.TrimSpace(string(data)), " ", 3)
	if len(f) < 2 || f[1] != hash {
		return false, false, ""
	}
	if f[0] == "ok" {
		return true, true, ""
	}
	if len(f) == 3 {
		msg = f[2]
	}
	return false, true, fwContainer + " could not apply the rules: " + msg
}

func (t *fwTarget) waitForFile(ctx context.Context, hash string) (bool, string) {
	wait := t.wait
	if wait <= 0 {
		wait = 10 * time.Second
	}
	deadline := time.Now().Add(wait)
	for {
		if ok, known, msg := t.fileStatus(hash); known {
			return ok, msg
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false, fwContainer + " has not applied the rules yet (is it running the trapdoor image?)"
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func (t *fwTarget) bin(name string) string {
	if t.iptablesDir != "" {
		return filepath.Join(t.iptablesDir, name)
	}
	return name
}

func (t *fwTarget) run(ctx context.Context, stdin, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, t.bin(name), args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return nil
}

// iptablesApply replaces the chain and makes sure INPUT and FORWARD jump
// to it first: traffic from clients to other networks (FORWARD) and to
// this host itself (INPUT, e.g. SSH on the server's VPN address).
func (t *fwTarget) iptablesApply(ctx context.Context, rules string) error {
	if err := t.run(ctx, rules, "iptables-restore", "--noflush"); err != nil {
		return err
	}
	for _, chain := range []string{"INPUT", "FORWARD"} {
		jump := []string{chain, "-i", t.iface, "-j", t.chain}
		if t.run(ctx, "", "iptables", append([]string{"-C"}, jump...)...) == nil {
			continue
		}
		if err := t.run(ctx, "", "iptables", append([]string{"-I", chain, "1"}, jump[1:]...)...); err != nil {
			return err
		}
	}
	return nil
}

// remove takes the chain out (the server is gone). Best effort.
func (t *fwTarget) remove(ctx context.Context) {
	if t.mode == FirewallDirect {
		for _, chain := range []string{"INPUT", "FORWARD"} {
			// Delete until gone (a jump can be there twice), but never loop forever.
			for range 8 {
				if t.run(ctx, "", "iptables", "-D", chain, "-i", t.iface, "-j", t.chain) != nil {
					break
				}
			}
		}
		_ = t.run(ctx, "", "iptables", "-F", t.chain)
		_ = t.run(ctx, "", "iptables", "-X", t.chain)
	}
	_ = os.Remove(t.rulesPath)
	_ = os.Remove(t.statusPath)
}
