package claudecli

import "fmt"

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
