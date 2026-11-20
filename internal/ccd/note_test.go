package ccd

import (
	"strings"
	"testing"
)

func TestNoteKeepsOtherLines(t *testing.T) {
	base := "#2FA_KEY: 3132\n#STATIC_PASS: x\nifconfig-push 10.8.0.10 255.255.255.0\n"
	s := SetNote(base, "  Anna's phone ")
	if Note(s) != "Anna's phone" {
		t.Fatalf("note = %q in %q", Note(s), s)
	}
	if !strings.Contains(s, base) {
		t.Fatalf("other lines lost: %q", s)
	}
	s = SetNote(s, "Laptop")
	if Note(s) != "Laptop" || strings.Count(s, notePrefix) != 1 {
		t.Fatalf("replace: %q", s)
	}
	if got := SetNote(s, ""); got != base {
		t.Fatalf("remove: %q, want %q", got, base)
	}
	if got := SetNote(SetNote("", "x"), ""); got != "" {
		t.Fatalf("only note removed: %q, want empty", got)
	}
}

func TestCheckNote(t *testing.T) {
	for _, bad := range []string{"a\nb", "a\rb", strings.Repeat("x", MaxNote+1)} {
		if CheckNote(bad) == nil {
			t.Errorf("CheckNote(%q) = nil", bad)
		}
	}
	if CheckNote("Anna's phone") != nil {
		t.Error("valid note rejected")
	}
}
