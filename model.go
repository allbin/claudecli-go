package claudecli

import (
	"fmt"
	"strings"
)

// EffortLevel controls reasoning intensity. WithEffort emits --effort <level>;
// how a given model uses it is the model's business.
type EffortLevel string

const (
	EffortLow    EffortLevel = "low"
	EffortMedium EffortLevel = "medium"
	EffortHigh   EffortLevel = "high"
	EffortXHigh  EffortLevel = "xhigh"
	EffortMax    EffortLevel = "max"

	// DefaultEffort matches the Claude Code CLI default.
	DefaultEffort = EffortXHigh
)

// Model represents a Claude model identifier.
type Model string

const (
	ModelHaiku  Model = "haiku"
	ModelSonnet Model = "sonnet"
	ModelOpus   Model = "opus"
	// ModelFable is Claude Fable 5, the most capable tier (above Opus).
	ModelFable Model = "fable"

	// DefaultModel is used when no model is specified.
	DefaultModel = ModelSonnet
)

// ModelDisplayName converts a model identifier into a human-readable name,
// e.g. "claude-opus-4-8" -> "Opus 4.8", "claude-haiku-4-5-20251001" -> "Haiku 4.5".
//
// The name is parsed from the ID's structure (tier + major.minor) rather than a
// lookup table, so new model releases render correctly without code changes. A
// trailing 8-digit date stamp and any bracketed suffix (e.g. "[1m]") are ignored.
// Bare aliases work too: "opus" -> "Opus". If no opus/sonnet/haiku tier is found,
// the input is returned unchanged (with any bracketed suffix stripped).
func ModelDisplayName(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	// Strip a bracketed context-window marker like "[1m]".
	if i := strings.IndexByte(id, '['); i >= 0 {
		id = strings.TrimSpace(id[:i])
	}

	var tier string
	var nums []string
	for _, tok := range strings.Split(strings.ToLower(id), "-") {
		switch tok {
		case "opus", "sonnet", "haiku":
			tier = tok
		default:
			if isAllDigits(tok) && len(tok) != 8 { // skip YYYYMMDD date stamps
				nums = append(nums, tok)
			}
		}
	}
	if tier == "" {
		return id // unrecognized format — return as-is
	}

	name := strings.ToUpper(tier[:1]) + tier[1:]
	if len(nums) > 0 {
		name += " " + strings.Join(nums, ".")
	}
	return name
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ThinkingConfig is a sealed interface for extended thinking configuration
// passed to WithThinking. Implementations: ThinkingAdaptive, ThinkingEnabled,
// ThinkingDisabled.
type ThinkingConfig interface {
	thinkingConfig()
	appendArgs(args *[]string)
}

// ThinkingAdaptive selects adaptive thinking. Emits --thinking adaptive.
type ThinkingAdaptive struct{}

func (ThinkingAdaptive) thinkingConfig() {}
func (ThinkingAdaptive) appendArgs(args *[]string) {
	*args = append(*args, "--thinking", "adaptive")
}

// ThinkingEnabled requests thinking with an explicit token budget.
// Emits --max-thinking-tokens <BudgetTokens> (the CLI infers enabled state
// from the flag). Whether a model honors the budget depends on the model.
type ThinkingEnabled struct {
	BudgetTokens int
}

func (ThinkingEnabled) thinkingConfig() {}
func (t ThinkingEnabled) appendArgs(args *[]string) {
	*args = append(*args, "--max-thinking-tokens", fmt.Sprintf("%d", t.BudgetTokens))
}

// ThinkingDisabled turns extended thinking off. Emits --thinking disabled.
type ThinkingDisabled struct{}

func (ThinkingDisabled) thinkingConfig() {}
func (ThinkingDisabled) appendArgs(args *[]string) {
	*args = append(*args, "--thinking", "disabled")
}
