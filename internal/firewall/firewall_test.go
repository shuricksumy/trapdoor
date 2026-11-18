package firewall

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
)

func valid(string) bool { return true }

func TestValidate(t *testing.T) {
	for name, r := range map[string]Rule{
		"action":     {Action: "maybe"},
		"proto":      {Action: Allow, Proto: "sctp"},
		"ports any":  {Action: Allow, Ports: "22"},
		"bad port":   {Action: Allow, Proto: ProtoTCP, Ports: "70000"},
		"range":      {Action: Allow, Proto: ProtoTCP, Ports: "90-80"},
		"many ports": {Action: Allow, Proto: ProtoTCP, Ports: "1,2,3,4,5,6,7,8,9,10,11,12,13,14,15-20"},
		"to":         {Action: Allow, To: "192.168.1.0/33"},
		"to v6":      {Action: Allow, To: "fd00::/64"},
		"note":       {Action: Allow, Note: "a\nb"},
	} {
		p := Policy{Rules: []Rule{r}}
		Normalize(&p)
		if err := Validate(p, valid); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	p := Policy{Rules: []Rule{{Client: "bob", Action: Allow}}}
	Normalize(&p)
	if err := Validate(p, func(n string) bool { return n == "alice" }); err == nil {
		t.Error("unknown client accepted")
	}
	ok := Policy{Default: Deny, Rules: []Rule{{To: "192.168.1.7", Proto: ProtoTCPUDP, Ports: "53, 80-90", Action: Allow}}}
	Normalize(&ok)
	if err := Validate(ok, valid); err != nil {
		t.Fatal(err)
	}
	if ok.Rules[0].To != "192.168.1.7/32" || ok.Rules[0].Ports != "53,80-90" {
		t.Errorf("normalized = %+v", ok.Rules[0])
	}
}

func TestRender(t *testing.T) {
	p := Policy{Enabled: true, Default: Deny, Rules: []Rule{
		{Client: "alice", To: "192.168.1.0/24", Action: Allow},
		{Client: "bob", To: "192.168.1.10", Proto: ProtoTCP, Ports: "22,8000-8100", Action: Allow},
		{Client: "carol", Action: Deny},
		{To: "192.168.1.1", Proto: ProtoTCPUDP, Ports: "53", Action: Allow},
		{To: "10.0.0.0/8", Action: Deny},
	}}
	Normalize(&p)
	res := Render(p, Clients{IPs: map[string]netip.Addr{
		"alice": netip.MustParseAddr("10.8.0.10"), "bob": netip.MustParseAddr("10.8.0.11"),
	}, ClientToClient: true})
	want := `# trapdoor access rules for VPN clients; written by the agent, do not edit
*filter
:TD-FWD - [0:0]
-F TD-FWD
-A TD-FWD -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
-A TD-FWD -s 10.8.0.10/32 -d 192.168.1.0/24 -j ACCEPT
-A TD-FWD -s 10.8.0.11/32 -d 192.168.1.10/32 -p tcp -m multiport --dports 22,8000:8100 -j ACCEPT
-A TD-FWD -d 192.168.1.1/32 -p tcp -m multiport --dports 53 -j ACCEPT
-A TD-FWD -d 192.168.1.1/32 -p udp -m multiport --dports 53 -j ACCEPT
-A TD-FWD -d 10.0.0.0/8 -j REJECT --reject-with icmp-admin-prohibited
-A TD-FWD -j REJECT --reject-with icmp-admin-prohibited
COMMIT
`
	if res.Rules != want {
		t.Errorf("rules:\n%s\nwant:\n%s", res.Rules, want)
	}
	if len(res.Missing) != 1 || res.Missing[0] != "carol" || len(res.Warnings) != 2 || !strings.Contains(res.Warnings[0], "rule 3") {
		t.Errorf("missing %v warnings %v", res.Missing, res.Warnings)
	}

	off := Render(Policy{Default: Deny, Rules: p.Rules}, Clients{})
	if !strings.Contains(off.Rules, "-A TD-FWD -j RETURN\nCOMMIT") || strings.Contains(off.Rules, "ACCEPT") {
		t.Errorf("disabled:\n%s", off.Rules)
	}
	allow := Render(Policy{Enabled: true, Default: Allow, Rules: []Rule{{Client: "x", Proto: ProtoTCP, Action: Deny}}}, Clients{IPs: map[string]netip.Addr{"x": netip.MustParseAddr("10.8.0.5")}})
	if !strings.Contains(allow.Rules, "-A TD-FWD -s 10.8.0.5/32 -p tcp -j REJECT --reject-with tcp-reset\n-A TD-FWD -j RETURN") {
		t.Errorf("allow default:\n%s", allow.Rules)
	}
}
