package claudecli

import (
	"encoding/json"
	"fmt"
)

// ContextCategory is one labelled slice of the context window.
type ContextCategory struct {
	Name   string `json:"name"`
	Tokens int    `json:"tokens"`
	// IsDeferred marks content the CLI has not loaded into the window yet
	// (e.g. deferred tool definitions).
	IsDeferred bool `json:"isDeferred"`
}

// ContextUsage is a breakdown of the session's current context window use, as
// answered by the get_context_usage control request.
//
// This is measured on demand against the live transcript, which is what makes
// it worth asking for: ResultEvent.ContextSnapshot and ResultEvent.ModelUsage
// describe the last API call, so both go stale the moment the CLI compacts.
type ContextUsage struct {
	// Model is the main-loop model the usage was computed for.
	Model string `json:"model"`
	// TotalTokens is the estimated tokens in use. Unclamped: it can exceed
	// RawMaxTokens when the session is over the limit.
	TotalTokens int `json:"totalTokens"`
	// MaxTokens is the window usage is reported against.
	MaxTokens int `json:"maxTokens"`
	// RawMaxTokens is the model's believed hard limit, which may be larger
	// than MaxTokens when a smaller compaction-policy window applies (for
	// example the 200K boundary on a 1M-window model).
	RawMaxTokens int `json:"rawMaxTokens"`
	// Percentage is TotalTokens over the resolved window, 0-100 and beyond.
	Percentage float64 `json:"percentage"`

	// IsAutoCompactEnabled reports whether the CLI will compact on its own.
	IsAutoCompactEnabled bool `json:"isAutoCompactEnabled"`
	// AutoCompactThreshold is the token count at which auto-compaction
	// triggers. Zero when absent.
	AutoCompactThreshold int `json:"autoCompactThreshold"`
	// AutocompactSource names which input decided the window (e.g.
	// "settings", "model-default"). Undocumented upstream as of CLI 2.1.235
	// but returned in practice, so it is carried as an opaque string.
	AutocompactSource string `json:"autocompactSource"`

	// Categories breaks the window down by bucket (system prompt, tools,
	// messages, and so on).
	Categories []ContextCategory `json:"categories"`

	// Raw is the complete response. The CLI returns considerably more detail
	// than is modelled here — per-MCP-tool and per-skill token attribution, a
	// message breakdown, and TUI rendering data — and the shape grows over
	// time. Decode from Raw for anything not covered by the typed fields.
	Raw json.RawMessage `json:"-"`
}

// Remaining reports the tokens left before the resolved window is reached.
// Zero once usage meets or exceeds the window.
func (c *ContextUsage) Remaining() int {
	if c.MaxTokens <= c.TotalTokens {
		return 0
	}
	return c.MaxTokens - c.TotalTokens
}

// QueryContextUsage asks the CLI for a live breakdown of context window usage.
//
// Prefer this over deriving context from a ResultEvent when the number needs
// to stay correct across compaction: result-derived usage describes the last
// API call and does not shrink when the CLI compacts, so it drifts upward
// until the next turn. Costs one control round-trip.
func (s *Session) QueryContextUsage() (*ContextUsage, error) {
	raw, err := s.sendControlRequestRaw("get_context_usage", nil)
	if err != nil {
		return nil, err
	}
	var usage ContextUsage
	if err := json.Unmarshal(raw, &usage); err != nil {
		return nil, fmt.Errorf("get_context_usage: decode response: %w", err)
	}
	usage.Raw = append(json.RawMessage(nil), raw...)
	return &usage, nil
}
