// Package clientauth implements the second factor for VPN clients, compatible
// with openvpn-ui: settings live as comment lines in the client's ccd file
//
//	#2FA_KEY:<hex secret>        TOTP, HMAC-SHA256, 6 digits, 30 s
//	#2FA_USER:<label>            account label shown in authenticator apps
//	#STATIC_PASS:<plain>         openvpn-ui's static password (still accepted)
//	#STATIC_PASS_HASH:<pbkdf2>   tunnelward's static password (hashed)
//
// The client logs in with username = its certificate name and
// password = static password followed by the current TOTP code (either part
// may be absent). OpenVPN runs `tunnelward auth-verify` to check it.
package clientauth

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/shuricksumy/tunnelward/internal/auth"
)

const (
	prefixKey        = "#2FA_KEY:"
	prefixUser       = "#2FA_USER:"
	prefixStatic     = "#STATIC_PASS:"
	prefixStaticHash = "#STATIC_PASS_HASH:"
	headerComment    = "#2FA OTP and/or static pass for auth."

	Digits = 6
	Period = 30 * time.Second
	// Skew accepts codes from this many steps before/after now (clock drift).
	Skew = 1
)

// Settings are one client's second-factor settings.
type Settings struct {
	TOTPKey     []byte // nil = no TOTP
	TOTPUser    string
	StaticPlain string // legacy openvpn-ui value
	StaticHash  string // tunnelward value
}

// Enabled reports whether any factor is configured.
func (s Settings) Enabled() bool {
	return len(s.TOTPKey) > 0 || s.StaticPlain != "" || s.StaticHash != ""
}

// HasStatic reports whether a static password is set.
func (s Settings) HasStatic() bool { return s.StaticPlain != "" || s.StaticHash != "" }

// Parse reads the 2FA lines of a ccd file. Prefixes are matched case-
// insensitively and the first occurrence wins, like the old shell script.
func Parse(ccd string) (Settings, error) {
	var s Settings
	seen := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(ccd))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		for _, p := range []string{prefixStaticHash, prefixKey, prefixUser, prefixStatic} {
			if len(line) < len(p) || !strings.EqualFold(line[:len(p)], p) || seen[p] {
				continue
			}
			seen[p] = true
			v := strings.TrimSpace(line[len(p):])
			switch p {
			case prefixKey:
				if v != "" {
					key, err := hex.DecodeString(v)
					if err != nil {
						return s, fmt.Errorf("2FA key is not hex: %w", err)
					}
					s.TOTPKey = key
				}
			case prefixUser:
				s.TOTPUser = v
			case prefixStatic:
				s.StaticPlain = v
			case prefixStaticHash:
				s.StaticHash = v
			}
			break
		}
	}
	return s, sc.Err()
}

// Apply returns ccd with its 2FA lines replaced by s; all other lines
// (routes, ifconfig-push, comments) are kept as they are.
func Apply(ccd string, s Settings) string {
	var kept []string
	for _, line := range strings.Split(ccd, "\n") {
		t := strings.TrimSpace(line)
		if strings.EqualFold(t, headerComment) || isAuthLine(t) {
			continue
		}
		kept = append(kept, line)
	}
	out := strings.TrimRight(strings.Join(kept, "\n"), "\n")
	if s.Enabled() {
		var b strings.Builder
		if out != "" {
			b.WriteString(out + "\n\n")
		}
		b.WriteString(headerComment + "\n")
		if len(s.TOTPKey) > 0 {
			b.WriteString(prefixKey + hex.EncodeToString(s.TOTPKey) + "\n")
			b.WriteString(prefixUser + s.TOTPUser + "\n")
		}
		if s.StaticHash != "" {
			b.WriteString(prefixStaticHash + s.StaticHash + "\n")
		} else if s.StaticPlain != "" {
			b.WriteString(prefixStatic + s.StaticPlain + "\n")
		}
		return b.String()
	}
	if out == "" {
		return ""
	}
	return out + "\n"
}

func isAuthLine(t string) bool {
	for _, p := range []string{prefixStaticHash, prefixKey, prefixUser, prefixStatic} {
		if len(t) >= len(p) && strings.EqualFold(t[:len(p)], p) {
			return true
		}
	}
	return false
}

// NewSecret returns a random 32-byte TOTP secret (as openvpn-ui used).
func NewSecret() ([]byte, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return b, err
}

// Code returns the TOTP code for key at time t (RFC 6238, HMAC-SHA256).
func Code(key []byte, t time.Time) string {
	return hotp(key, uint64(t.Unix()/int64(Period/time.Second)))
}

func hotp(key []byte, counter uint64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(sha256.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", Digits, v%1_000_000)
}

// checkCode accepts the code of the current step or Skew steps around it.
func checkCode(key []byte, code string, now time.Time) bool {
	if len(code) != Digits {
		return false
	}
	step := now.Unix() / int64(Period/time.Second)
	ok := 0
	for d := -Skew; d <= Skew; d++ {
		ok |= subtle.ConstantTimeCompare([]byte(hotp(key, uint64(step+int64(d)))), []byte(code))
	}
	return ok == 1
}

// ErrDenied is returned for any failed verification.
var ErrDenied = errors.New("access denied")

// Verify checks a password (static + TOTP code) against s.
// With no factor configured only an empty password is accepted, as in
// openvpn-ui.
func Verify(s Settings, password string, now time.Time) error {
	static := password
	if len(s.TOTPKey) > 0 {
		if len(password) < Digits {
			return ErrDenied
		}
		static, code := password[:len(password)-Digits], password[len(password)-Digits:]
		if !checkCode(s.TOTPKey, code, now) {
			return ErrDenied
		}
		return verifyStatic(s, static)
	}
	return verifyStatic(s, static)
}

func verifyStatic(s Settings, given string) error {
	switch {
	case s.StaticHash != "":
		ok, err := auth.VerifyPassword(s.StaticHash, given)
		if err != nil || !ok {
			return ErrDenied
		}
	case s.StaticPlain != "":
		if subtle.ConstantTimeCompare([]byte(given), []byte(s.StaticPlain)) != 1 {
			return ErrDenied
		}
	default:
		if given != "" {
			return ErrDenied
		}
	}
	return nil
}

// HashStatic hashes a new static password.
func HashStatic(pw string) (string, error) {
	if l := len(pw); l < 4 || l > 128 {
		return "", errors.New("static password must be 4-128 characters")
	}
	if strings.ContainsAny(pw, "\r\n") {
		return "", errors.New("static password must not contain line breaks")
	}
	return auth.HashPassword(pw)
}

// URI is the otpauth:// URI for authenticator apps (shown as a QR code).
func URI(issuer, account string, key []byte) string {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(key)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA256")
	q.Set("digits", fmt.Sprint(Digits))
	q.Set("period", fmt.Sprint(int(Period/time.Second)))
	label := url.PathEscape(issuer + ":" + account)
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// Base32 returns the secret in the form authenticator apps accept for manual entry.
func Base32(key []byte) string {
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(key)
}
