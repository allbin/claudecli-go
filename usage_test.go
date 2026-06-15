package claudecli

import "testing"

func TestUsageTotalTokens(t *testing.T) {
	tests := []struct {
		name  string
		usage Usage
		want  int
	}{
		{"zero", Usage{}, 0},
		{"only input", Usage{InputTokens: 100}, 100},
		{
			"all fields",
			Usage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 30, CacheCreateTokens: 40},
			100,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.usage.TotalTokens(); got != tt.want {
				t.Errorf("TotalTokens() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestUsageString(t *testing.T) {
	u := Usage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 30, CacheCreateTokens: 40}
	want := "Usage{in: 10, out: 20, cacheRead: 30, cacheCreate: 40, total: 100}"
	if got := u.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestModelUsageTotalTokens(t *testing.T) {
	m := ModelUsage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 30, CacheCreateTokens: 40}
	if got, want := m.TotalTokens(), 100; got != want {
		t.Errorf("TotalTokens() = %d, want %d", got, want)
	}
}
