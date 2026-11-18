package agent

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/config"
	"github.com/shuricksumy/trapdoor/internal/firewall"
)

// fakeIPTables writes iptables / iptables-restore scripts that log calls.
func fakeIPTables(t *testing.T, fail bool) (dir, log string) {
	t.Helper()
	dir = t.TempDir()
	log = filepath.Join(dir, "calls")
	rc := "0"
	if fail {
		rc = "1"
	}
	restore := "#!/bin/sh\necho restore \"$@\" >>" + log + "\ncat >" + dir + "/applied\n[ " + rc + " = 0 ] || { echo 'iptables-restore: line 5 failed' >&2; exit 1; }\n"
	// -C fails the first time so the jump gets inserted once.
	ipt := "#!/bin/sh\necho iptables \"$@\" >>" + log + "\nif [ \"$1\" = -C ]; then grep -q -- \"-I $2 \" " + log + "; exit $?; fi\n"
	for name, body := range map[string]string{"iptables-restore": restore, "iptables": ipt} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir, log
}

func firewallClient(t *testing.T, cfg config.Agent) (*Client, string) {
	t.Helper()
	dir := t.TempDir()
	cfg.Token, cfg.OpenVPNDir, cfg.CCDDir = "secret", dir, filepath.Join(dir, "ccd")
	_ = os.Mkdir(cfg.CCDDir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "server.conf"), []byte("server 10.8.0.0 255.255.255.0\ntopology subnet\nclient-config-dir ccd\n"), 0o644)
	_ = os.WriteFile(filepath.Join(cfg.CCDDir, "alice"), []byte("ifconfig-push 10.8.0.10 255.255.255.0\n"), 0o644)
	s := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	s.mgmt = &fakeMgmt{}
	s.restartWait = 2 * time.Second
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "secret", 10*time.Second), dir
}

func TestFirewallDirect(t *testing.T) {
	ipt, calls := fakeIPTables(t, false)
	c, dir := firewallClient(t, config.Agent{Firewall: "direct", IPTablesDir: ipt})
	ctx := context.Background()

	st, err := c.Firewall(ctx)
	if err != nil || st.Policy.Enabled || !st.Applied || st.Subnet != "10.8.0.0/24" {
		t.Fatalf("initial = %+v, %v", st, err)
	}
	if _, err := os.Stat(calls); err == nil {
		t.Fatal("iptables touched without a saved policy")
	}

	p := api.FirewallPolicy{Enabled: true, Default: firewall.Deny, Rules: []api.FirewallRule{
		{Client: "alice", To: "192.168.1.0/24", Action: firewall.Allow},
		{Client: "bob", To: "192.168.1.5", Proto: "tcp", Ports: "22", Action: firewall.Allow},
	}}
	if _, err := c.SetFirewall(ctx, api.FirewallPolicy{Default: "maybe"}); err == nil {
		t.Error("invalid policy accepted")
	}
	st, err = c.SetFirewall(ctx, p)
	if err != nil || !st.Applied || st.Error != "" || len(st.Missing) != 1 || st.Missing[0] != "bob" {
		t.Fatalf("set = %+v, %v", st, err)
	}
	applied, _ := os.ReadFile(filepath.Join(ipt, "applied"))
	if !strings.Contains(string(applied), "-A TD-FWD -s 10.8.0.10/32 -d 192.168.1.0/24 -j ACCEPT") {
		t.Errorf("applied:\n%s", applied)
	}
	log, _ := os.ReadFile(calls)
	if !strings.Contains(string(log), "restore --noflush") || strings.Count(string(log), "-I FORWARD 1 -i tun+ -j TD-FWD") != 1 || strings.Count(string(log), "-I INPUT 1 -i tun+ -j TD-FWD") != 1 {
		t.Errorf("calls:\n%s", log)
	}

	// Give bob an address: the rule becomes active.
	st, err = c.AssignAddresses(ctx, []string{"bob"})
	if err != nil || len(st.Missing) != 0 || !strings.Contains(st.Rules, "-s 10.8.0.254/32 -d 192.168.1.5/32 -p tcp") {
		t.Fatalf("assign = %+v, %v", st, err)
	}
	ccdBob, _ := os.ReadFile(filepath.Join(dir, "ccd", "bob"))
	if !strings.Contains(string(ccdBob), "ifconfig-push 10.8.0.254 255.255.255.0") {
		t.Errorf("ccd/bob = %q", ccdBob)
	}
	log, _ = os.ReadFile(calls)
	if strings.Count(string(log), "-I FORWARD") != 1 {
		t.Errorf("jump inserted twice:\n%s", log)
	}
	if st, _ := c.Firewall(ctx); !st.Applied {
		t.Errorf("get after assign = %+v", st)
	}
}

func TestFirewallDirectError(t *testing.T) {
	ipt, _ := fakeIPTables(t, true)
	c, _ := firewallClient(t, config.Agent{Firewall: "direct", IPTablesDir: ipt})
	st, err := c.SetFirewall(context.Background(), api.FirewallPolicy{Enabled: true, Default: "deny"})
	if err != nil || st.Applied || !strings.Contains(st.Error, "line 5 failed") {
		t.Fatalf("set = %+v, %v", st, err)
	}
}

func TestFirewallFile(t *testing.T) {
	c, dir := firewallClient(t, config.Agent{Firewall: "file"})
	ctx := context.Background()
	// Play the OpenVPN container: apply whatever rules file appears.
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
			}
			if data, err := os.ReadFile(filepath.Join(dir, "trapdoor-firewall.rules")); err == nil {
				_ = os.WriteFile(filepath.Join(dir, "trapdoor-firewall.status"), []byte("ok "+firewall.Hash(string(data))+"\n"), 0o644)
			}
		}
	}()
	st, err := c.SetFirewall(ctx, api.FirewallPolicy{Enabled: true, Default: "allow", Rules: []api.FirewallRule{{To: "10.0.0.0/8", Action: "deny"}}})
	if err != nil || !st.Applied || st.Mode != "file" {
		t.Fatalf("set = %+v, %v", st, err)
	}
	if st, _ := c.Firewall(ctx); !st.Applied {
		t.Errorf("get = %+v", st)
	}
}
