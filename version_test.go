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

func TestNormalizeVersion(t *testing.T) {
	tests := []struct{ in, want string }{
		{"v1.2.3", "1.2.3"},
		{"1.2.3", "1.2.3"},
		{"v0.0.0-20240101120000-abcdef123456", "0.0.0-20240101120000-abcdef123456"},
		{"(devel)", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := normalizeVersion(tt.in); got != tt.want {
			t.Errorf("normalizeVersion(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSDKVersion_FallbackInDevBuild(t *testing.T) {
	// Under `go test` this repo is the main module with version "(devel)", so
	// sdkVersion cannot resolve a real version and falls back to SDKVersion.
	if got := sdkVersion(); got != SDKVersion {
		t.Errorf("sdkVersion() = %q, want dev fallback %q", got, SDKVersion)
	}
	if SDKVersion == "" {
		t.Error("SDKVersion fallback must be non-empty; the CLI env var must never be blank")
	}
}
