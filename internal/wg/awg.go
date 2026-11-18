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
	if level != api.AWGLevel15 {
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

// ValidateObfuscation checks settings entered by an admin.
func ValidateObfuscation(o api.Obfuscation) error {
	ranges := o.Level == api.AWGLevel20
	if o.Level != api.AWGLevel15 && !ranges {
		return errors.New("level must be 1.5 or 2.0")
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
	if client {
		str("I1", o.I1)
		str("I2", o.I2)
		str("I3", o.I3)
		str("I4", o.I4)
		str("I5", o.I5)
	}
	return out
}
