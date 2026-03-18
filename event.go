package claudecli

import (
	"encoding/json"
	"fmt"
	"time"
)

// Event is a sealed interface representing a Claude CLI stream event.
// Consumers use type switches or type assertions to access event data.
type Event interface {
	event()
}

// StartEvent is emitted by the client before the CLI process starts.
// Contains the resolved configuration for observability.
type StartEvent struct {
	Model   Model
	Args    []string
	WorkDir string
}

func (*StartEvent) event() {}
func (e *StartEvent) String() string {
	return fmt.Sprintf("StartEvent{Model: %s, WorkDir: %s}", e.Model, e.WorkDir)
}

// InitEvent is emitted by the CLI at the start of a session.
type InitEvent struct {
	SessionID string
	Model     string
	Tools     []string
}

func (*InitEvent) event() {}
func (e *InitEvent) String() string {
	return fmt.Sprintf("InitEvent{SessionID: %s, Model: %s}", e.SessionID, e.Model)
}

// ThinkingEvent contains extended thinking output.
type ThinkingEvent struct {
	Content   string
	Signature string
}

func (*ThinkingEvent) event() {}
func (e *ThinkingEvent) String() string {
	return fmt.Sprintf("ThinkingEvent{len: %d}", len(e.Content))
}

// TextEvent contains assistant text output.
type TextEvent struct {
	Content string
}

func (*TextEvent) event() {}
func (e *TextEvent) String() string {
	return fmt.Sprintf("TextEvent{len: %d}", len(e.Content))
}

// ToolUseEvent is emitted when the assistant invokes a tool.
type ToolUseEvent struct {
	ID    string
	Name  string
	Input json.RawMessage
}

func (*ToolUseEvent) event() {}
func (e *ToolUseEvent) String() string {
	return fmt.Sprintf("ToolUseEvent{Name: %s, ID: %s}", e.Name, e.ID)
}

// ToolResultEvent contains the result of a tool invocation.
type ToolResultEvent struct {
	ToolUseID string
	Content   string
}

func (*ToolResultEvent) event() {}
func (e *ToolResultEvent) String() string {
	return fmt.Sprintf("ToolResultEvent{ToolUseID: %s}", e.ToolUseID)
}

// RateLimitEvent is emitted when the CLI reports rate limit status.
type RateLimitEvent struct {
	Status      string
	Utilization float64
}

func (*RateLimitEvent) event() {}
func (e *RateLimitEvent) String() string {
	return fmt.Sprintf("RateLimitEvent{Status: %s, Utilization: %.2f}", e.Status, e.Utilization)
}

// StderrEvent contains a line of stderr output from the CLI process.
type StderrEvent struct {
	Content string
}

func (*StderrEvent) event() {}
func (e *StderrEvent) String() string {
	return fmt.Sprintf("StderrEvent{%s}", e.Content)
}

// ResultEvent is emitted at the end of a successful session.
type ResultEvent struct {
	Text             string
	Subtype          string
	StopReason       string
	StructuredOutput json.RawMessage
	Duration         time.Duration
	CostUSD          float64
	SessionID        string
	Usage            Usage
}

func (*ResultEvent) event() {}
func (e *ResultEvent) String() string {
	if e.StopReason != "" {
		return fmt.Sprintf("ResultEvent{Cost: $%.4f, Duration: %s, Tokens: %d/%d, StopReason: %s}",
			e.CostUSD, e.Duration, e.Usage.InputTokens, e.Usage.OutputTokens, e.StopReason)
	}
	return fmt.Sprintf("ResultEvent{Cost: $%.4f, Duration: %s, Tokens: %d/%d}",
		e.CostUSD, e.Duration, e.Usage.InputTokens, e.Usage.OutputTokens)
}

// ErrorEvent is emitted when an error occurs during streaming.
// Fatal errors (process failures) transition the stream to StateFailed.
// Non-fatal errors (e.g. malformed JSONL) are emitted but don't affect state.
type ErrorEvent struct {
	Err   error
	Fatal bool
}

func (*ErrorEvent) event() {}
func (e *ErrorEvent) String() string {
	return fmt.Sprintf("ErrorEvent{Fatal: %v, Err: %v}", e.Fatal, e.Err)
}
func (e *ErrorEvent) Error() string { return e.Err.Error() }
func (e *ErrorEvent) Unwrap() error { return e.Err }

// ControlRequestEvent is emitted when the CLI sends a control request.
// In session mode, these are handled internally and not exposed.
type ControlRequestEvent struct {
	RequestID string
	Subtype   string
	Body      json.RawMessage
}

func (*ControlRequestEvent) event() {}
func (e *ControlRequestEvent) String() string {
	return fmt.Sprintf("ControlRequestEvent{RequestID: %s, Subtype: %s}", e.RequestID, e.Subtype)
}

// StreamEvent represents a partial message update (when include_partial_messages is on).
type StreamEvent struct {
	UUID      string
	SessionID string
	Event     json.RawMessage
}

func (*StreamEvent) event() {}
func (e *StreamEvent) String() string {
	return fmt.Sprintf("StreamEvent{UUID: %s}", e.UUID)
}

// Usage contains token usage statistics.
type Usage struct {
	InputTokens       int
	OutputTokens      int
	CacheReadTokens   int
	CacheCreateTokens int
}
