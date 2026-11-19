package wg

import (
	"strings"
	"testing"

	"github.com/shuricksumy/trapdoor/internal/api"
)

func TestGenerateObfuscation(t *testing.T) {
	for _, level := range []string{api.AWGLevel15, api.AWGLevel20, api.AWGLevel31} {
		for range 500 {
			o := GenerateObfuscation(level)
			if err := ValidateObfuscation(o); err != nil {
				t.Fatalf("%s: generated %+v: %v", level, o, err)
			}
			if o.Jc < 4 || o.Jc > 12 || o.S1 < 15 || o.S1 > 150 || o.S2 < 15 || o.S2 > 150 {
				t.Fatalf("outside the recommended ranges: %+v", o)
			}
			ranged := strings.Contains(o.H1, "-")
			if (level != api.AWGLevel15) != ranged || (level == api.AWGLevel15) != (o.S3 == 0 && o.S4 == 0) {
				t.Fatalf("%s: wrong shape %+v", level, o)
			}
			if v31 := level == api.AWGLevel31; v31 != (o.HeaderProtectionKey != "") || v31 != o.RandomTrailers || v31 != (o.RekeyAfterTime != "") {
				t.Fatalf("%s: 3.1 settings %+v", level, o)
			}
			if level == api.AWGLevel31 && (o.S4 < 12 || !ValidKey(o.HeaderProtectionKey)) {
				t.Fatalf("3.1: header protection needs S4 >= 12 and a key: %+v", o)
			}
		}
	}
	if GenerateObfuscation("").Level != api.AWGLevel20 {
		t.Error("default level is not 2.0")
	}
}

func TestValidateObfuscation(t *testing.T) {
	ok := api.Obfuscation{Level: api.AWGLevel20, Jc: 4, Jmin: 40, Jmax: 70, S1: 52, S2: 83, S3: 21, S4: 9,
		H1: "1000000-1999999", H2: "2000000-2999999", H3: "3000000", H4: "4000000-4999999", I1: "<b 0xc700000001><r 16><t>"}
	if err := ValidateObfuscation(ok); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*api.Obfuscation){
		"level":         func(o *api.Obfuscation) { o.Level = "3" },
		"jc":            func(o *api.Obfuscation) { o.Jc = 200 },
		"jmin >= jmax":  func(o *api.Obfuscation) { o.Jmin = 70 },
		"jmax too big":  func(o *api.Obfuscation) { o.Jmax = 1500 },
		"s1+56 == s2":   func(o *api.Obfuscation) { o.S2 = o.S1 + 56 },
		"s1 too big":    func(o *api.Obfuscation) { o.S1 = 2000 },
		"s4 too big":    func(o *api.Obfuscation) { o.S4 = 1000 },
		"h overlap":     func(o *api.Obfuscation) { o.H2 = "1500000-2500000" },
		"h same":        func(o *api.Obfuscation) { o.H3 = "4000000" },
		"h not number":  func(o *api.Obfuscation) { o.H1 = "abc" },
		"h reversed":    func(o *api.Obfuscation) { o.H1 = "9-5" },
		"h too big":     func(o *api.Obfuscation) { o.H1 = "5000000000" },
		"i1 bad tag":    func(o *api.Obfuscation) { o.I1 = "<x 1>" },
		"i1 odd hex":    func(o *api.Obfuscation) { o.I1 = "<b 0xabc>" },
		"i1 newline":    func(o *api.Obfuscation) { o.I1 = "<t>\n[Peer]" },
		"i1 too random": func(o *api.Obfuscation) { o.I2 = "<r 5000>" },
	} {
		o := ok
		change(&o)
		if err := ValidateObfuscation(o); err == nil {
			t.Errorf("%s: accepted %+v", name, o)
		}
	}
	old := api.Obfuscation{Level: api.AWGLevel15, Jc: 4, Jmin: 40, Jmax: 70, S1: 52, S2: 83, H1: "5", H2: "6", H3: "7", H4: "8"}
	if err := ValidateObfuscation(old); err != nil {
		t.Errorf("level 1.5: %v", err)
	}
	for _, bad := range []api.Obfuscation{
		func() api.Obfuscation { o := old; o.S3 = 10; return o }(),
		func() api.Obfuscation { o := old; o.H1 = "5-9"; return o }(),
	} {
		if ValidateObfuscation(bad) == nil {
			t.Errorf("level 1.5 accepted 2.0 settings: %+v", bad)
		}
	}
}

func TestObfuscationSettings(t *testing.T) {
	o := api.Obfuscation{Level: api.AWGLevel20, Jc: 4, Jmin: 40, Jmax: 70, S1: 52, S2: 83, S3: 21, S4: 9,
		H1: "1-2", H2: "3-4", H3: "5-6", H4: "7-8", I1: "<t>"}
	srv := ServerConfig(Server{PrivateKey: "K", Extra: ObfuscationSettings(o, false)}, nil)
	for _, want := range []string{"Jc = 4\nJmin = 40\nJmax = 70\nS1 = 52\nS2 = 83\nS3 = 21\nS4 = 9\nH1 = 1-2\nH2 = 3-4\nH3 = 5-6\nH4 = 7-8\n"} {
		if !strings.Contains(srv, want) {
			t.Errorf("server config:\n%s", srv)
		}
	}
	if strings.Contains(srv, "I1") {
		t.Error("I1 in the server config")
	}
	cli := ClientConfig(Client{PrivateKey: "C", Extra: ObfuscationSettings(o, true)})
	if !strings.Contains(cli, "H4 = 7-8\nI1 = <t>\n") {
		t.Errorf("client config:\n%s", cli)
	}
	plain := ObfuscationSettings(api.Obfuscation{H1: "1"}, true)
	if len(plain) != 1 {
		t.Errorf("zero values not left out: %+v", plain)
	}
}

func TestObfuscation31(t *testing.T) {
	o := GenerateObfuscation(api.AWGLevel31)
	o.ContentPadding = "10-40"
	if err := ValidateObfuscation(o); err != nil {
		t.Fatal(err)
	}
	if PaddingMax(o) != 40 {
		t.Errorf("padding max = %d", PaddingMax(o))
	}
	for name, change := range map[string]func(*api.Obfuscation){
		"bad key":            func(o *api.Obfuscation) { o.HeaderProtectionKey = "nope" },
		"small s1":           func(o *api.Obfuscation) { o.S1 = 11 },
		"padding too big":    func(o *api.Obfuscation) { o.ContentPadding = "10-5000" },
		"padding reversed":   func(o *api.Obfuscation) { o.ContentPadding = "40-10" },
		"zero timing":        func(o *api.Obfuscation) { o.RekeyTimeout = "0-5" },
		"rekey after reject": func(o *api.Obfuscation) { o.RekeyAfterTime = "150-190" },
		"timing not number":  func(o *api.Obfuscation) { o.KeepaliveTimeout = "soon" },
	} {
		x := o
		change(&x)
		if ValidateObfuscation(x) == nil {
			t.Errorf("%s: accepted %+v", name, x)
		}
	}
	// Small S values are fine without header protection.
	x := o
	x.HeaderProtectionKey, x.S1 = "", 5
	if err := ValidateObfuscation(x); err != nil {
		t.Errorf("no header protection: %v", err)
	}
	// 3.1 settings need level 3.1.
	x = o
	x.Level = api.AWGLevel20
	if ValidateObfuscation(x) == nil {
		t.Error("3.1 settings accepted at 2.0")
	}

	srv := ServerConfig(Server{PrivateKey: "K", Extra: ObfuscationSettings(o, false)}, nil)
	cli := ClientConfig(Client{PrivateKey: "C", Extra: ObfuscationSettings(o, true)})
	both := []string{"HeaderProtectionKey = " + o.HeaderProtectionKey + "\n", "RandomTrailers = on\n", "ContentPaddingAddition = 10-40\n"}
	for _, want := range append(both, "DisableCookies = on\n") {
		if !strings.Contains(srv, want) {
			t.Errorf("server config lacks %q:\n%s", want, srv)
		}
	}
	for _, want := range append(both, "RekeyAfterTime = 100-140\n", "RejectAfterTime = 180-200\n", "RekeyTimeout = 4-6\n", "KeepaliveTimeout = 8-12\n") {
		if !strings.Contains(cli, want) {
			t.Errorf("client config lacks %q:\n%s", want, cli)
		}
	}
	if strings.Contains(srv, "RekeyAfterTime") || strings.Contains(cli, "DisableCookies") {
		t.Errorf("one-sided settings on the wrong side:\nserver:\n%s\nclient:\n%s", srv, cli)
	}
}
