package claudecli

// EffortLevel controls the thinking effort for the model.
type EffortLevel string

const (
	EffortLow    EffortLevel = "low"
	EffortMedium EffortLevel = "medium"
	EffortHigh   EffortLevel = "high"
	EffortMax    EffortLevel = "max"
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
