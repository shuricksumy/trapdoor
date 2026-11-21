package shaping

import (
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIFB(t *testing.T) {
	for in, want := range map[string]string{"openvpn": "tq-openvpn", "wg-office-long": "tq-wg-office-lo", "WG1": "tq-wg1"} {
		if got := IFB(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}

func TestRenderEmpty(t *testing.T) {
	s, _ := Render("tun+", "tq-openvpn", []Client{{Name: "anna", Addr: netip.MustParseAddr("10.8.0.6")}})
	if strings.Contains(s, " add ") {
		t.Errorf("nothing limited, but rules:\n%s", s)
	}
	if !strings.Contains(s, "tc qdisc del dev \"$DEV\" root") || !strings.Contains(s, "# IFACE=tun+") {
		t.Errorf("no cleanup:\n%s", s)
	}
}

func TestRender(t *testing.T) {
	s, skipped := Render("wg0", "tq-wg0", []Client{
		{Name: "bob", Addr: netip.MustParseAddr("10.8.0.7"), Limit: Limit{Down: 1000}},
		{Name: "anna", Addr: netip.MustParseAddr("10.8.0.6"), Limit: Limit{Down: 50000, Up: 10000}},
		{Name: "eve\nrm -rf /", Addr: netip.MustParseAddr("10.8.0.9"), Limit: Limit{Block: true, Down: 5}},
		{Name: "v6", Addr: netip.MustParseAddr("fd00::2"), Limit: Limit{Down: 1000}},
		{Name: "free", Addr: netip.MustParseAddr("10.8.0.8")},
	})
	if len(skipped) != 1 || skipped[0] != "v6" {
		t.Errorf("skipped %v", skipped)
	}
	for _, want := range []string{
		// download: classes in address order
		`tc class add dev "$DEV" parent 1: classid 1:10 htb rate 50000kbit ceil 50000kbit quantum 200000 # anna`,
		`tc filter add dev "$DEV" parent 1: protocol ip prio 2 u32 match ip dst 10.8.0.6/32 flowid 1:10`,
		`tc class add dev "$DEV" parent 1: classid 1:11 htb rate 1000kbit ceil 1000kbit quantum 12500 # bob`,
		`tc filter add dev "$DEV" parent 1: protocol ip prio 1 u32 match ip dst 10.8.0.9/32 action drop # eve rm -rf / blocked`,
		// upload: only anna has a limit; eve dropped at ingress
		`tc filter add dev "$DEV" parent ffff: protocol ip prio 1 u32 match ip src 10.8.0.9/32 action drop`,
		`tc class add dev "$IFB" parent 1: classid 1:10 htb rate 10000kbit ceil 10000kbit quantum 125000 # anna`,
		`tc filter add dev "$IFB" parent 1: protocol ip prio 2 u32 match ip src 10.8.0.6/32 flowid 1:10`,
		`action mirred egress redirect dev "$IFB"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "10.8.0.8") || strings.Contains(s, "\nrm -rf") {
		t.Errorf("unexpected line:\n%s", s)
	}
}

// The script runs with sh against fake tc / ip that record their arguments.
func TestRenderRuns(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	for _, name := range []string{"tc", "ip"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\necho \""+name+" $*\" >>"+log+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := Render("tun+", "tq-openvpn", []Client{{Name: "anna", Addr: netip.MustParseAddr("10.8.0.6"), Limit: Limit{Down: 1000, Up: 500}}})
	path := filepath.Join(dir, "shaping.sh")
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		_ = os.Remove(log)
		cmd := exec.Command("sh", append([]string{path}, args...)...)
		cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		b, _ := os.ReadFile(log)
		return string(b)
	}
	got := run("tun0")
	for _, want := range []string{"tc qdisc add dev tun0 root handle 1: htb default 0", "ip link add tq-openvpn type ifb", "tc qdisc add dev tq-openvpn root handle 1: htb default 0", "redirect dev tq-openvpn"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if got := run("tun0", "clear"); strings.Contains(got, " add ") || !strings.Contains(got, "ip link del tq-openvpn") {
		t.Errorf("clear:\n%s", got)
	}
}
