package claudecli

import (
	"context"
	"testing"
)

func TestParseSemver(t *testing.T) {
	tests := []struct {
		input         string
		maj, min, pat int
		ok            bool
	}{
		{"2.1.3", 2, 1, 3, true},
		{"v2.1.3", 2, 1, 3, true},
		{"2.1.3-beta", 2, 1, 3, true},
		{"abc", 0, 0, 0, false},
		{"", 0, 0, 0, false},
		{"1.2", 0, 0, 0, false},
	}
	for _, tt := range tests {
		maj, min, pat, ok := parseSemver(tt.input)
		if maj != tt.maj || min != tt.min || pat != tt.pat || ok != tt.ok {
			t.Errorf("parseSemver(%q) = (%d,%d,%d,%v), want (%d,%d,%d,%v)",
				tt.input, maj, min, pat, ok, tt.maj, tt.min, tt.pat, tt.ok)
		}
	}
}

func TestCompareSemver(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"2.0.0", "2.0.0", 0},
		{"2.1.0", "2.0.0", 1},
		{"1.9.9", "2.0.0", -1},
		{"2.0.1", "2.0.0", 1},
	}
	for _, tt := range tests {
		got := compareSemver(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("compareSemver(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestCheckCLIVersion_InvalidBinary(t *testing.T) {
	err := CheckCLIVersion(context.Background(), "/nonexistent/claude-binary-xxx")
	if err != nil {
		t.Errorf("expected nil (fail-open), got %v", err)
	}
}
