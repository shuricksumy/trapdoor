package clientauth

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// RFC 6238 appendix B test vectors for HMAC-SHA256 (8 digits there; we use
// the last 6, which is what a 6-digit implementation produces).
func TestCodeRFC6238(t *testing.T) {
	key := []byte("12345678901234567890123456789012")
	for _, tc := range []struct {
		unix int64
		want string // last 6 digits of the RFC's 8-digit values
	}{
		{59, "119246"},          // 46119246
		{1111111109, "084774"},  // 68084774
		{1111111111, "062674"},  // 67062674
		{1234567890, "819424"},  // 91819424
		{2000000000, "698825"},  // 90698825
		{20000000000, "737706"}, // 77737706
	} {
		if got := Code(key, time.Unix(tc.unix, 0)); got != tc.want {
			t.Errorf("Code(%d) = %s, want %s", tc.unix, got, tc.want)
		}
	}
}

const legacyCCD = `ifconfig-push 10.8.0.50 255.255.255.0
# Route to NAS [home] device internal subnet
push "route 192.168.1.0 255.255.255.0"

#2FA OTP and/or static pass for auth.
#2FA_KEY:` + "3132333435363738393031323334353637383930313233343536373839303132" + `
#2FA_USER:alice@home
#STATIC_PASS:pin1
`

func TestParseLegacy(t *testing.T) {
	s, err := Parse(legacyCCD)
	if err != nil {
		t.Fatal(err)
	}
	if string(s.TOTPKey) != "12345678901234567890123456789012" || s.TOTPUser != "alice@home" || s.StaticPlain != "pin1" {
		t.Fatalf("settings = %+v", s)
	}
	// Case-insensitive prefixes, like grep -i in the old script.
	s, _ = Parse("#2fa_key:00ff\n#static_pass:x\n")
	if hex.EncodeToString(s.TOTPKey) != "00ff" || s.StaticPlain != "x" {
		t.Errorf("case-insensitive parse = %+v", s)
	}
	if _, err := Parse("#2FA_KEY:not-hex\n"); err == nil {
		t.Error("bad hex accepted")
	}
}

func TestVerify(t *testing.T) {
	s, _ := Parse(legacyCCD)
	now := time.Unix(1234567890, 0)
	code := Code(s.TOTPKey, now)

	cases := []struct {
		pw   string
		want bool
	}{
		{"pin1" + code, true},
		{"pin1" + Code(s.TOTPKey, now.Add(-30*time.Second)), true}, // clock drift
		{"pin1" + Code(s.TOTPKey, now.Add(30*time.Second)), true},
		{"pin1" + Code(s.TOTPKey, now.Add(-90*time.Second)), false}, // too old
		{"pin2" + code, false},
		{code, false},
		{"pin1", false},
		{"", false},
	}
	for _, c := range cases {
		if got := Verify(s, c.pw, now) == nil; got != c.want {
			t.Errorf("Verify(%q) = %v, want %v", c.pw, got, c.want)
		}
	}

	// TOTP only, static only, hashed static, nothing.
	totp := Settings{TOTPKey: s.TOTPKey}
	if Verify(totp, code, now) != nil || Verify(totp, "x"+code, now) == nil {
		t.Error("TOTP-only verification wrong")
	}
	hash, err := HashStatic("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	hashed := Settings{StaticHash: hash}
	if Verify(hashed, "s3cret", now) != nil || Verify(hashed, "s3cre", now) == nil {
		t.Error("hashed static verification wrong")
	}
	if Verify(Settings{}, "", now) != nil || Verify(Settings{}, "anything", now) == nil {
		t.Error("no factor: only the empty password passes (openvpn-ui behaviour)")
	}
}

func TestApplyKeepsOtherLines(t *testing.T) {
	key, _ := NewSecret()
	hash, _ := HashStatic("pin2")
	out := Apply(legacyCCD, Settings{TOTPKey: key, TOTPUser: "alice", StaticHash: hash})
	for _, want := range []string{`ifconfig-push 10.8.0.50 255.255.255.0`, `push "route 192.168.1.0 255.255.255.0"`,
		"#2FA_KEY:" + hex.EncodeToString(key), "#2FA_USER:alice", "#STATIC_PASS_HASH:pbkdf2-sha256$"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "#STATIC_PASS:pin1") || strings.Count(out, headerComment) != 1 {
		t.Errorf("old auth lines not replaced:\n%s", out)
	}
	back, _ := Parse(out)
	if hex.EncodeToString(back.TOTPKey) != hex.EncodeToString(key) || back.StaticPlain != "" || back.StaticHash != hash {
		t.Errorf("round trip = %+v", back)
	}

	// Disabling removes only the 2FA part.
	off := Apply(out, Settings{})
	if strings.Contains(off, "2FA") || strings.Contains(off, "STATIC") || !strings.Contains(off, "ifconfig-push") {
		t.Errorf("disable:\n%s", off)
	}
	if Apply("#2FA_KEY:00\n", Settings{}) != "" {
		t.Error("ccd with only 2FA lines should become empty")
	}
}

func TestURI(t *testing.T) {
	u := URI("Home VPN", "alice", []byte("12345678901234567890"))
	for _, want := range []string{"otpauth://totp/Home%20VPN:alice?", "secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", "algorithm=SHA256", "digits=6", "period=30", "issuer=Home+VPN"} {
		if !strings.Contains(u, want) {
			t.Errorf("uri %s missing %q", u, want)
		}
	}
}

func TestVerifyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "alice"), []byte(legacyCCD), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bob"), []byte("#STATIC_PASS:bobpin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1234567890, 0)
	s, _ := Parse(legacyCCD)
	creds := func(user, pass string) string {
		p := filepath.Join(t.TempDir(), "creds")
		_ = os.WriteFile(p, []byte(user+"\n"+pass+"\n"), 0o600)
		return p
	}

	if _, err := VerifyFile(dir, creds("alice", "pin1"+Code(s.TOTPKey, now)), "alice", now); err != nil {
		t.Errorf("good login: %v", err)
	}
	// Bob's valid credentials on Alice's certificate must fail (the old script allowed this).
	if _, err := VerifyFile(dir, creds("bob", "bobpin"), "alice", now); !errors.Is(err, ErrDenied) {
		t.Errorf("cross-certificate login: err = %v", err)
	}
	for _, c := range [][2]string{{"alice", "wrong"}, {"../alice", "x"}, {"carol", ""}} {
		if _, err := VerifyFile(dir, creds(c[0], c[1]), c[0], now); !errors.Is(err, ErrDenied) {
			t.Errorf("%v: err = %v", c, err)
		}
	}
	if _, err := VerifyFile(dir, filepath.Join(dir, "missing"), "alice", now); err == nil {
		t.Error("missing credentials file accepted")
	}
}
