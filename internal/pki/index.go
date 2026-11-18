package pki

import (
	"fmt"
	"strings"
	"time"
)

// indexEntry is one line of an OpenSSL "ca" database (index.txt):
//
//	status \t expiry \t revocation[,reason] \t serial \t filename \t subject
type indexEntry struct {
	Status    byte // 'V', 'R' or 'E'
	NotAfter  time.Time
	RevokedAt time.Time
	Reason    string
	Serial    string // uppercase hex
	File      string // usually "unknown"
	Subject   string // e.g. "/CN=alice"
}

// CN returns the common name from the subject.
func (e indexEntry) CN() string {
	for _, part := range strings.Split(e.Subject, "/") {
		if v, ok := strings.CutPrefix(part, "CN="); ok {
			return v
		}
	}
	return ""
}

// status computes the effective status at time now.
func (e indexEntry) status(now time.Time) Status {
	switch {
	case e.Status == 'R':
		return StatusRevoked
	case e.Status == 'E' || !now.Before(e.NotAfter):
		return StatusExpired
	default:
		return StatusValid
	}
}

const (
	utcTimeLayout         = "060102150405Z"
	generalizedTimeLayout = "20060102150405Z"
)

// parseTime parses the ASN.1 UTCTime / GeneralizedTime strings openssl writes.
func parseTime(s string) (time.Time, error) {
	switch len(s) {
	case len(utcTimeLayout):
		// UTCTime: years 50-99 are 1950-1999 (RFC 5280); Go's 2-digit
		// year rule (69-99 → 19xx) differs, so fix it up explicitly.
		t, err := time.Parse(utcTimeLayout, s)
		if err != nil {
			return t, err
		}
		if y := t.Year(); y >= 2050 {
			t = t.AddDate(-100, 0, 0)
		} else if y < 1950 {
			t = t.AddDate(100, 0, 0)
		}
		return t, nil
	case len(generalizedTimeLayout):
		return time.Parse(generalizedTimeLayout, s)
	default:
		return time.Time{}, fmt.Errorf("unexpected time %q", s)
	}
}

// formatTime formats t the way openssl does: UTCTime before 2050,
// GeneralizedTime after.
func formatTime(t time.Time) string {
	t = t.UTC()
	if t.Year() < 2050 {
		return t.Format(utcTimeLayout)
	}
	return t.Format(generalizedTimeLayout)
}

// parseIndex parses the contents of index.txt.
func parseIndex(data string) ([]indexEntry, error) {
	var out []indexEntry
	for n, line := range strings.Split(data, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		e, err := parseIndexLine(line)
		if err != nil {
			return nil, fmt.Errorf("index.txt line %d: %w", n+1, err)
		}
		out = append(out, e)
	}
	return out, nil
}

func parseIndexLine(line string) (indexEntry, error) {
	f := strings.Split(line, "\t")
	if len(f) != 6 {
		return indexEntry{}, fmt.Errorf("want 6 tab-separated fields, got %d", len(f))
	}
	var e indexEntry
	if len(f[0]) != 1 || !strings.Contains("VRE", f[0]) {
		return e, fmt.Errorf("unknown status %q", f[0])
	}
	e.Status = f[0][0]

	var err error
	if e.NotAfter, err = parseTime(f[1]); err != nil {
		return e, fmt.Errorf("expiry: %w", err)
	}
	if f[2] != "" {
		date, reason, _ := strings.Cut(f[2], ",")
		if e.RevokedAt, err = parseTime(date); err != nil {
			return e, fmt.Errorf("revocation date: %w", err)
		}
		e.Reason = reason
	}
	if e.Status == 'R' && e.RevokedAt.IsZero() {
		return e, fmt.Errorf("revoked entry without revocation date")
	}
	e.Serial = strings.ToUpper(f[3])
	e.File = f[4]
	e.Subject = f[5]
	if e.Serial == "" || e.Subject == "" {
		return e, fmt.Errorf("empty serial or subject")
	}
	return e, nil
}

// formatIndex renders entries back to index.txt format.
func formatIndex(entries []indexEntry) string {
	var b strings.Builder
	for _, e := range entries {
		rev := ""
		if !e.RevokedAt.IsZero() {
			rev = formatTime(e.RevokedAt)
			if e.Reason != "" {
				rev += "," + e.Reason
			}
		}
		file := e.File
		if file == "" {
			file = "unknown"
		}
		fmt.Fprintf(&b, "%c\t%s\t%s\t%s\t%s\t%s\n",
			e.Status, formatTime(e.NotAfter), rev, e.Serial, file, e.Subject)
	}
	return b.String()
}
