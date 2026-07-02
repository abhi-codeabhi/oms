package ids

import (
	"strings"
	"testing"
)

func TestNewPrefixAndValidity(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
	}{
		{"outlet", "out"},
		{"user", "usr"},
		{"order", "ord"},
		{"event", "evt"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := New(tc.prefix)
			if !strings.HasPrefix(id, tc.prefix+"_") {
				t.Fatalf("New(%q) = %q, want prefix %q_", tc.prefix, id, tc.prefix)
			}
			if !Valid(tc.prefix, id) {
				t.Fatalf("Valid(%q, %q) = false, want true", tc.prefix, id)
			}
		})
	}
}

func TestNewUnique(t *testing.T) {
	a := New("out")
	b := New("out")
	if a == b {
		t.Fatalf("expected unique ids, both = %q", a)
	}
}

func TestValid(t *testing.T) {
	good := New("out")
	tests := []struct {
		name   string
		prefix string
		id     string
		want   bool
	}{
		{"valid", "out", good, true},
		{"wrong prefix", "usr", good, false},
		{"missing underscore", "out", "out01HX", false},
		{"empty", "out", "", false},
		{"bad suffix length", "out", "out_short", false},
		{"non-base32 suffix", "out", "out_" + strings.Repeat("!", 26), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Valid(tc.prefix, tc.id); got != tc.want {
				t.Fatalf("Valid(%q, %q) = %v, want %v", tc.prefix, tc.id, got, tc.want)
			}
		})
	}
}
