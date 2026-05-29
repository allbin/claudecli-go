package claudecli

import "testing"

func TestModelDisplayName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		// Current 4.x snapshot IDs.
		{"claude-opus-4-8", "Opus 4.8"},
		{"claude-sonnet-4-6", "Sonnet 4.6"},
		{"claude-haiku-4-5-20251001", "Haiku 4.5"},
		{"claude-opus-4-1-20250805", "Opus 4.1"},
		// Context-window marker is stripped.
		{"claude-opus-4-8[1m]", "Opus 4.8"},
		// Bare aliases have no version.
		{"opus", "Opus"},
		{"sonnet", "Sonnet"},
		{"haiku", "Haiku"},
		// Legacy 3.x ordering still yields a sensible name.
		{"claude-3-5-sonnet-20241022", "Sonnet 3.5"},
		{"claude-3-opus-20240229", "Opus 3"},
		// Unrecognized tier returns the (bracket-stripped) input.
		{"gpt-4o", "gpt-4o"},
		{"claude-next-9-0[1m]", "claude-next-9-0"},
		{"", ""},
		{"  claude-opus-4-8  ", "Opus 4.8"},
	}
	for _, tt := range tests {
		if got := ModelDisplayName(tt.in); got != tt.want {
			t.Errorf("ModelDisplayName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestInitEventModelDisplayName(t *testing.T) {
	e := &InitEvent{Model: "claude-opus-4-8"}
	if got, want := e.ModelDisplayName(), "Opus 4.8"; got != want {
		t.Errorf("InitEvent.ModelDisplayName() = %q, want %q", got, want)
	}
}
