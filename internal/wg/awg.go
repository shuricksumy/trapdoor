package wg

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"github.com/shuricksumy/trapdoor/internal/api"
)

// AmneziaWG obfuscation: limits from the AmneziaWG documentation
// (amneziawg-linux-kernel-module README, docs.amnezia.org). They assume an
// MTU of 1280 for the junk packet size.
const (
	maxJc    = 128
	maxJunk  = 1280
	maxS1    = 1132
	maxS2    = 1188
	maxS3    = 1000 // not documented; keeps cookie packets sane
	maxS4    = 256  // not documented; every data packet carries it
	minH     = 5    // 1-4 are WireGuard's own message types
	maxH     = 1<<31 - 1
	maxTag   = 1000 // <r N>, <rc N>, <rd N>
	s1s2Diff = 56   // S1 + 56 must not equal S2 (init and response would look alike)
	minSHP   = 12   // header protection uses S1-S4 as nonce: at least 12 each
	maxPad   = 256  // content padding, kept small: it comes off the MTU
	maxTime  = 3600 // client timings, seconds
)

// randInt returns a uniform number in [lo, hi].
func randInt(lo, hi int) int {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(hi-lo+1)))
	if err != nil {
		panic(err) // crypto/rand does not fail on supported systems
	}
	return lo + int(n.Int64())
}

// GenerateObfuscation returns random settings for the level, inside the
// documented limits. I1-I5 stay empty: a believable imitation depends on
// the network and is up to the admin.
func GenerateObfuscation(level string) api.Obfuscation {
	if level != api.AWGLevel15 && level != api.AWGLevel31 {
		level = api.AWGLevel20
	}
	o := api.Obfuscation{Level: level, Jc: randInt(4, 12), Jmin: randInt(40, 80)}
	o.Jmax = randInt(o.Jmin+20, o.Jmin+200)
	o.S1 = randInt(15, 150)
	for o.S2 = randInt(15, 150); o.S1+s1s2Diff == o.S2; o.S2 = randInt(15, 150) {
	}
	hs := make([]string, 4)
	if level == api.AWGLevel15 {
		used := map[int]bool{}
		for i := range hs {
			v := randInt(minH, maxH)
			for used[v] {
				v = randInt(minH, maxH)
			}
			used[v] = true
			hs[i] = strconv.Itoa(v)
		}
	} else {
		o.S3, o.S4 = randInt(15, 150), randInt(8, 32)
		if level == api.AWGLevel31 {
			// Header protection needs S4 >= 12 too; trailers, no cookie
			// replies and timings around WireGuard's own (120 / 180 / 5 / 10 s).
			o.S4 = randInt(minSHP, 32)
			k, err := GeneratePSK() // 32 random bytes, like `awg genkey`
			if err != nil {
				panic(err) // crypto/rand does not fail on supported systems
			}
			o.HeaderProtectionKey = k
			o.RandomTrailers, o.DisableCookies = true, true
			o.RekeyAfterTime, o.RejectAfterTime = "100-140", "180-200"
			o.RekeyTimeout, o.KeepaliveTimeout = "4-6", "8-12"
		}
		// One range in each quarter of the value space, in random order.
		band := (maxH - minH) / 4
		order := []int{0, 1, 2, 3}
		for i := 3; i > 0; i-- {
			j := randInt(0, i)
			order[i], order[j] = order[j], order[i]
		}
		for i, b := range order {
			lo := minH + b*band
			start := randInt(lo, lo+band/2)
			hs[i] = fmt.Sprintf("%d-%d", start, randInt(start+1_000_000, lo+band-1))
		}
	}
	o.H1, o.H2, o.H3, o.H4 = hs[0], hs[1], hs[2], hs[3]
	return o
}

// tagPattern is one I-packet: tags <b 0xHEX>, <r N>, <rc N>, <rd N>, <t>.
var tagPattern = regexp.MustCompile(`^(?:<b 0x(?:[0-9a-fA-F]{2})+>|<r \d+>|<rc \d+>|<rd \d+>|<t>)+$`)

var tagSize = regexp.MustCompile(`<r[cd]? (\d+)>`)

func parseH(s string, ranges bool) (lo, hi uint64, err error) {
	a, b, isRange := strings.Cut(strings.TrimSpace(s), "-")
	if isRange && !ranges {
		return 0, 0, errors.New("must be one number at level 1.5")
	}
	if lo, err = strconv.ParseUint(a, 10, 32); err != nil {
		return 0, 0, errors.New("must be a number" + map[bool]string{true: " or a range a-b", false: ""}[ranges])
	}
	hi = lo
	if isRange {
		if hi, err = strconv.ParseUint(b, 10, 32); err != nil || hi < lo {
			return 0, 0, errors.New("range must be a-b with a <= b")
		}
	}
	return lo, hi, nil
}

// parseRange reads "a" or "a-b" (a <= b <= max).
func parseRange(s string, max uint64) (lo, hi uint64, err error) {
	lo, hi, err = parseH(s, true)
	if err == nil && hi > max {
		err = fmt.Errorf("at most %d", max)
	}
	return lo, hi, err
}

// ValidateObfuscation checks settings entered by an admin.
func ValidateObfuscation(o api.Obfuscation) error {
	v31 := o.Level == api.AWGLevel31
	ranges := o.Level == api.AWGLevel20 || v31
	if o.Level != api.AWGLevel15 && !ranges {
		return errors.New("level must be 1.5, 2.0 or 3.1")
	}
	if err := validate31(o, v31); err != nil {
		return err
	}
	if o.Jc < 0 || o.Jc > maxJc {
		return fmt.Errorf("jc must be 0-%d (recommended 4-12)", maxJc)
	}
	if o.Jc > 0 && (o.Jmin < 0 || o.Jmin >= o.Jmax || o.Jmax > maxJunk) {
		return fmt.Errorf("junk sizes: 0 <= jmin < jmax <= %d", maxJunk)
	}
	if o.S1 < 0 || o.S1 > maxS1 || o.S2 < 0 || o.S2 > maxS2 {
		return fmt.Errorf("s1 must be 0-%d and s2 0-%d", maxS1, maxS2)
	}
	if o.S1+s1s2Diff == o.S2 {
		return fmt.Errorf("s2 must not be s1 + %d", s1s2Diff)
	}
	if !ranges && (o.S3 != 0 || o.S4 != 0) {
		return errors.New("s3 and s4 need level 2.0")
	}
	if o.S3 < 0 || o.S3 > maxS3 || o.S4 < 0 || o.S4 > maxS4 {
		return fmt.Errorf("s3 must be 0-%d and s4 0-%d", maxS3, maxS4)
	}
	type span struct{ lo, hi uint64 }
	var spans []span
	for i, h := range []string{o.H1, o.H2, o.H3, o.H4} {
		lo, hi, err := parseH(h, ranges)
		if err != nil {
			return fmt.Errorf("h%d %w", i+1, err)
		}
		for j, s := range spans {
			if lo <= s.hi && s.lo <= hi {
				return fmt.Errorf("h%d overlaps h%d: the four values must all differ", i+1, j+1)
			}
		}
		spans = append(spans, span{lo, hi})
	}
	for i, p := range []string{o.I1, o.I2, o.I3, o.I4, o.I5} {
		if p == "" {
			continue
		}
		if !tagPattern.MatchString(p) {
			return fmt.Errorf("i%d: use tags <b 0xHEX>, <r N>, <rc N>, <rd N> and <t>", i+1)
		}
		for _, m := range tagSize.FindAllStringSubmatch(p, -1) {
			if n, _ := strconv.Atoi(m[1]); n > maxTag {
				return fmt.Errorf("i%d: random parts can be at most %d bytes", i+1, maxTag)
			}
		}
	}
	return nil
}

// validate31 checks the AmneziaWG 3.1 settings (only allowed at 3.1).
func validate31(o api.Obfuscation, v31 bool) error {
	used := o.HeaderProtectionKey != "" || o.RandomTrailers || o.ContentPadding != "" || o.DisableCookies ||
		o.RekeyAfterTime != "" || o.RejectAfterTime != "" || o.RekeyTimeout != "" || o.KeepaliveTimeout != ""
	if !v31 {
		if used {
			return errors.New("header protection, random trailers, content padding, cookies and timings need level 3.1")
		}
		return nil
	}
	if o.HeaderProtectionKey != "" {
		if !ValidKey(o.HeaderProtectionKey) {
			return errors.New("header_protection_key must be a key like `awg genkey` prints")
		}
		if o.S1 < minSHP || o.S2 < minSHP || o.S3 < minSHP || o.S4 < minSHP {
			return fmt.Errorf("header protection needs s1-s4 of at least %d", minSHP)
		}
	}
	if o.ContentPadding != "" {
		if _, _, err := parseRange(o.ContentPadding, maxPad); err != nil {
			return fmt.Errorf("content_padding: %w", err)
		}
	}
	for _, t := range []struct{ name, v string }{
		{"rekey_after_time", o.RekeyAfterTime}, {"reject_after_time", o.RejectAfterTime},
		{"rekey_timeout", o.RekeyTimeout}, {"keepalive_timeout", o.KeepaliveTimeout},
	} {
		if t.v == "" {
			continue
		}
		if lo, _, err := parseRange(t.v, maxTime); err != nil || lo == 0 {
			return fmt.Errorf("%s: seconds as a or a-b, 1-%d", t.name, maxTime)
		}
	}
	if o.RekeyAfterTime != "" && o.RejectAfterTime != "" {
		_, rekey, _ := parseRange(o.RekeyAfterTime, maxTime)
		reject, _, _ := parseRange(o.RejectAfterTime, maxTime)
		if rekey >= reject {
			return errors.New("rekey_after_time must end before reject_after_time starts")
		}
	}
	return nil
}

// PaddingMax is the most bytes content padding adds (comes off the MTU).
func PaddingMax(o api.Obfuscation) int {
	if o.ContentPadding == "" {
		return 0
	}
	_, hi, err := parseRange(o.ContentPadding, maxPad)
	if err != nil || hi > maxPad {
		return 0
	}
	return int(hi)
}

// ObfuscationSettings are the [Interface] lines for o. The client also
// gets I1-I5: it sends them before its handshake.
func ObfuscationSettings(o api.Obfuscation, client bool) []Setting {
	var out []Setting
	num := func(k string, v int) {
		if v != 0 {
			out = append(out, Setting{k, strconv.Itoa(v)})
		}
	}
	str := func(k, v string) {
		if v != "" {
			out = append(out, Setting{k, v})
		}
	}
	num("Jc", o.Jc)
	if o.Jc > 0 {
		num("Jmin", o.Jmin)
		num("Jmax", o.Jmax)
	}
	num("S1", o.S1)
	num("S2", o.S2)
	num("S3", o.S3)
	num("S4", o.S4)
	str("H1", o.H1)
	str("H2", o.H2)
	str("H3", o.H3)
	str("H4", o.H4)
	on := func(k string, v bool) {
		if v {
			out = append(out, Setting{k, "on"})
		}
	}
	// 3.1: the same on both sides, except cookies (server) and timings (client).
	str("HeaderProtectionKey", o.HeaderProtectionKey)
	on("RandomTrailers", o.RandomTrailers)
	str("ContentPaddingAddition", o.ContentPadding)
	if !client {
		on("DisableCookies", o.DisableCookies)
	}
	if client {
		str("RekeyAfterTime", o.RekeyAfterTime)
		str("RejectAfterTime", o.RejectAfterTime)
		str("RekeyTimeout", o.RekeyTimeout)
		str("KeepaliveTimeout", o.KeepaliveTimeout)
		str("I1", o.I1)
		str("I2", o.I2)
		str("I3", o.I3)
		str("I4", o.I4)
		str("I5", o.I5)
	}
	return out
}
