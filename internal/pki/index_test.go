package pki

import (
	"strings"
	"testing"
	"time"
)

// Copied from a PKI produced by easyrsa 3.2.7.
const easyrsaIndex = "V\t290220134626Z\t\t8E5D792D78AAAB1D68E75B8725D58AEB\tunknown\t/CN=server\n" +
	"V\t290220134627Z\t\t752F920B025ABFA1A0C62D396CD99E7A\tunknown\t/CN=alice\n" +
	"R\t290220134627Z\t261118134627Z\t2FF20A3DED906752BF8ECD13476C0531\tunknown\t/CN=bob\n" +
	"R\t290220134627Z\t261118134700Z,superseded\t3A\tunknown\t/C=IE/CN=carol/emailAddress=c@example.com\n"

func TestParseIndexRoundTrip(t *testing.T) {
	entries, err := parseIndex(easyrsaIndex)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("got %d entries", len(entries))
	}
	if got := formatIndex(entries); got != easyrsaIndex {
		t.Fatalf("round trip mismatch:\n%s\nwant:\n%s", got, easyrsaIndex)
	}

	bob := entries[2]
	if bob.CN() != "bob" || bob.Status != 'R' {
		t.Errorf("bob = %+v", bob)
	}
	if want := time.Date(2026, 11, 18, 13, 46, 27, 0, time.UTC); !bob.RevokedAt.Equal(want) {
		t.Errorf("RevokedAt = %v, want %v", bob.RevokedAt, want)
	}
	if entries[3].CN() != "carol" || entries[3].Reason != "superseded" {
		t.Errorf("carol = %+v", entries[3])
	}
}

func TestStatus(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		e    indexEntry
		want Status
	}{
		{indexEntry{Status: 'V', NotAfter: now.Add(time.Hour)}, StatusValid},
		{indexEntry{Status: 'V', NotAfter: now.Add(-time.Hour)}, StatusExpired},
		{indexEntry{Status: 'E', NotAfter: now.Add(time.Hour)}, StatusExpired},
		{indexEntry{Status: 'R', NotAfter: now.Add(-time.Hour)}, StatusRevoked},
	}
	for _, c := range cases {
		if got := c.e.status(now); got != c.want {
			t.Errorf("%c/%v: got %s, want %s", c.e.Status, c.e.NotAfter, got, c.want)
		}
	}
}

func TestTimeFormats(t *testing.T) {
	for _, tc := range []struct {
		s    string
		want time.Time
	}{
		{"491231235959Z", time.Date(2049, 12, 31, 23, 59, 59, 0, time.UTC)},
		{"500101000000Z", time.Date(1950, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"690101000000Z", time.Date(1969, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"20510101000000Z", time.Date(2051, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		got, err := parseTime(tc.s)
		if err != nil || !got.Equal(tc.want) {
			t.Errorf("parseTime(%q) = %v, %v; want %v", tc.s, got, err, tc.want)
		}
		if f := formatTime(tc.want); f != tc.s {
			t.Errorf("formatTime(%v) = %q, want %q", tc.want, f, tc.s)
		}
	}
}

func TestParseIndexErrors(t *testing.T) {
	for _, bad := range []string{
		"V\t290220134626Z\t8E5D\tunknown\t/CN=x",   // 5 fields
		"X\t290220134626Z\t\t8E5D\tunknown\t/CN=x", // bad status
		"V\tsoon\t\t8E5D\tunknown\t/CN=x",          // bad time
		"R\t290220134626Z\t\t8E5D\tunknown\t/CN=x", // R without date
		"V\t290220134626Z\t\t\tunknown\t/CN=x",     // empty serial
	} {
		if _, err := parseIndex(bad); err == nil {
			t.Errorf("expected error for %q", strings.ReplaceAll(bad, "\t", "→"))
		}
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"alice", "bob.phone", "user_1", "a-b", "me@home"} {
		if !ValidName(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", ".hidden", "../x", "a/b", "a b", "ca", "x;rm", strings.Repeat("a", 65)} {
		if ValidName(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}
