package claudecli

import "fmt"

// EffortLevel controls reasoning intensity. On Opus 4.7+ this drives adaptive
// thinking (the model decides when and how much to think per step). On earlier
// models it maps to extended thinking with a fixed budget.
type EffortLevel string

const (
	EffortLow    EffortLevel = "low"
	EffortMedium EffortLevel = "medium"
	EffortHigh   EffortLevel = "high"
	EffortXHigh  EffortLevel = "xhigh"
	EffortMax    EffortLevel = "max"

	// DefaultEffort is the Claude Code default since Opus 4.7.
	DefaultEffort = EffortXHigh
)

// Model represents a Claude model identifier.
type Model string

const (
	ModelHaiku  Model = "haiku"
	ModelSonnet Model = "sonnet"
	ModelOpus   Model = "opus"

	// DefaultModel is used when no model is specified.
	DefaultModel = ModelSonnet
)

// ThinkingConfig is a sealed interface for extended thinking configuration
// passed to WithThinking. Implementations: ThinkingAdaptive, ThinkingEnabled,
// ThinkingDisabled.
type ThinkingConfig interface {
	thinkingConfig()
	appendArgs(args *[]string)
}

// ThinkingAdaptive selects adaptive thinking mode (the only mode on Opus 4.7+).
// Emits --thinking adaptive.
type ThinkingAdaptive struct{}

func (ThinkingAdaptive) thinkingConfig() {}
func (ThinkingAdaptive) appendArgs(args *[]string) {
	*args = append(*args, "--thinking", "adaptive")
}

// ThinkingEnabled selects extended thinking with an explicit token budget.
// Emits --max-thinking-tokens <BudgetTokens> (matches the Python SDK's path
// for enabled mode — the CLI infers enabled state from the flag).
// Primarily for pre-Opus 4.7 models; ignored or treated as adaptive on 4.7+.
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
