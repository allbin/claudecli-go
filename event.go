package claudecli

import (
	"encoding/json"
	"fmt"
	"strings"
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

// MCPServerStatus describes a connected MCP server and its connection state.
type MCPServerStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// InitEvent is emitted by the CLI at the start of a session.
type InitEvent struct {
	SessionID  string
	Model      string
	Tools      []string
	Agents     []string
	Skills     []string
	MCPServers []MCPServerStatus
}

func (*InitEvent) event() {}
func (e *InitEvent) String() string {
	return fmt.Sprintf("InitEvent{SessionID: %s, Model: %s}", e.SessionID, e.Model)
}

// CompactStatusEvent is emitted when the CLI's compaction status changes.
// Status is "compacting" when compaction starts, or "" when cleared.
type CompactStatusEvent struct {
	SessionID string
	Status    string
}

func (*CompactStatusEvent) event() {}
func (e *CompactStatusEvent) String() string {
	return fmt.Sprintf("CompactStatusEvent{Status: %q}", e.Status)
}

// CompactBoundaryEvent marks the compaction boundary.
// Trigger is "manual" (user invoked /compact) or "auto" (context limit).
// PreTokens is the token count before compaction.
// Raw contains the full compact_metadata JSON for forward compatibility.
type CompactBoundaryEvent struct {
	SessionID string
	Trigger   string
	PreTokens int
	Raw       json.RawMessage
}

func (*CompactBoundaryEvent) event() {}
func (e *CompactBoundaryEvent) String() string {
	return fmt.Sprintf("CompactBoundaryEvent{Trigger: %s, PreTokens: %d}", e.Trigger, e.PreTokens)
}

// TaskEvent is emitted for subagent lifecycle updates (system subtypes
// "task_started", "task_progress", "task_notification").
//
// ToolUseID links to the parent Agent ToolUseEvent.ID that spawned this task.
// TaskID is a unique identifier for the subagent task instance.
//
// Subtype meanings:
//   - "task_started": subagent spawned. Description, TaskType, Prompt are set.
//   - "task_progress": subagent working. Usage fields update, LastToolName shows current tool.
//   - "task_notification": subagent finished. Status ("completed"), Summary, final Usage.
type TaskEvent struct {
	Subtype   string // "task_started", "task_progress", "task_notification"
	TaskID    string
	ToolUseID string // parent Agent ToolUseEvent.ID
	SessionID string

	// task_started
	Description string
	TaskType    string // e.g. "local_agent"
	Prompt      string

	// task_progress
	LastToolName string

	// task_notification
	Status  string
	Summary string

	// task_progress + task_notification
	TotalTokens int
	ToolUses    int
	DurationMs  int

	// Raw contains the full JSON line for forward compatibility.
	Raw json.RawMessage
}

func (*TaskEvent) event() {}
func (e *TaskEvent) String() string {
	return fmt.Sprintf("TaskEvent{Subtype: %s, TaskID: %s, ToolUseID: %s}", e.Subtype, e.TaskID, e.ToolUseID)
}

// HookEvent is emitted when the CLI runs a configured hook (SessionStart,
// PreToolUse, PostToolUse, etc.). Subtype is "hook_started" when the hook
// begins and "hook_response" when it finishes.
//
// On "hook_started" only HookID, HookName, HookEvent, UUID, and SessionID
// are populated.
//
// On "hook_response" Output, Stdout, Stderr, ExitCode, and Outcome are also
// populated. Outcome is typically "success" or "failure".
type HookEvent struct {
	Subtype   string // "hook_started" or "hook_response"
	HookID    string
	HookName  string
	HookEvent string // e.g. "SessionStart", "PreToolUse"
	UUID      string
	SessionID string

	// hook_response only
	Output   string
	Stdout   string
	Stderr   string
	ExitCode int
	Outcome  string

	// Raw contains the full JSON line for forward compatibility.
	Raw json.RawMessage
}

func (*HookEvent) event() {}
func (e *HookEvent) String() string {
	if e.Subtype == "hook_response" {
		return fmt.Sprintf("HookEvent{%s %s outcome=%s exit=%d}", e.Subtype, e.HookName, e.Outcome, e.ExitCode)
	}
	return fmt.Sprintf("HookEvent{%s %s}", e.Subtype, e.HookName)
}

// ThinkingEvent contains model thinking output (extended thinking on pre-4.7
// models, adaptive thinking on Opus 4.7+).
//
// On Claude Opus 4.7, adaptive thinking defaults to display:"omitted" — the
// model thinks but the text is hidden. The SDK surfaces this as Content=""
// with Signature set. Callers should treat a non-empty Signature with empty
// Content as "thinking hidden", not "no thinking occurred".
//
// ParentToolUseID is set when this event comes from a subagent (links to the
// parent Agent ToolUseEvent.ID). Empty for top-level assistant turns.
type ThinkingEvent struct {
	Content         string
	Signature       string
	ParentToolUseID string
}

func (*ThinkingEvent) event() {}
func (e *ThinkingEvent) String() string {
	return fmt.Sprintf("ThinkingEvent{len: %d}", len(e.Content))
}

// TextEvent contains assistant text output.
// ParentToolUseID is set when this event comes from a subagent (links to the
// parent Agent ToolUseEvent.ID). Empty for top-level assistant turns.
type TextEvent struct {
	Content         string
	ParentToolUseID string
}

func (*TextEvent) event() {}
func (e *TextEvent) String() string {
	return fmt.Sprintf("TextEvent{len: %d}", len(e.Content))
}

// TurnEvent is emitted when a new assistant turn starts.
// Turn is a 1-based counter incremented for each top-level assistant message.
// ToolName is the name of the first tool_use block in the turn, or empty if
// the turn contains only text/thinking.
type TurnEvent struct {
	Turn     int
	ToolName string
}

func (*TurnEvent) event() {}
func (e *TurnEvent) String() string {
	if e.ToolName != "" {
		return fmt.Sprintf("TurnEvent{Turn: %d, Tool: %s}", e.Turn, e.ToolName)
	}
	return fmt.Sprintf("TurnEvent{Turn: %d}", e.Turn)
}

// ToolUseEvent is emitted when the assistant invokes a tool.
// ParentToolUseID is set when this event comes from a subagent (links to the
// parent Agent ToolUseEvent.ID). Empty for top-level assistant turns.
type ToolUseEvent struct {
	ID              string
	Name            string
	Input           json.RawMessage
	ParentToolUseID string
}

func (*ToolUseEvent) event() {}
func (e *ToolUseEvent) String() string {
	return fmt.Sprintf("ToolUseEvent{Name: %s, ID: %s}", e.Name, e.ID)
}

// AgentInput contains the parsed fields from an Agent tool invocation.
type AgentInput struct {
	Description     string `json:"description"`
	Prompt          string `json:"prompt"`
	SubagentType    string `json:"subagent_type"`
	Name            string `json:"name"`
	RunInBackground bool   `json:"run_in_background"`
	Model           string `json:"model"`
	Isolation       string `json:"isolation"`
	Mode            string `json:"mode"`
	TeamName        string `json:"team_name"`
}

// ParseAgentInput extracts structured fields from an Agent tool_use event.
// Returns nil if the event is not an Agent tool call or input is malformed.
func (e *ToolUseEvent) ParseAgentInput() *AgentInput {
	if e.Name != "Agent" {
		return nil
	}
	var a AgentInput
	if err := json.Unmarshal(e.Input, &a); err != nil {
		return nil
	}
	return &a
}

// ToolContent represents a single content block inside a tool result.
// Use the Type field to distinguish between block kinds.
type ToolContent struct {
	Type string // "text" or "image"

	// Text block fields.
	Text string // populated when Type == "text"

	// Image block fields.
	MediaType string // e.g. "image/png"; populated when Type == "image"
	Data      string // base64-encoded image data; populated when Type == "image"
}

// ToolResultEvent contains the result of a tool invocation.
// ParentToolUseID is set when this event comes from a subagent (links to the
// parent Agent ToolUseEvent.ID). Empty for top-level assistant turns.
type ToolResultEvent struct {
	ToolUseID       string
	Content         []ToolContent
	ParentToolUseID string
}

func (*ToolResultEvent) event() {}
func (e *ToolResultEvent) String() string {
	return fmt.Sprintf("ToolResultEvent{ToolUseID: %s, Blocks: %d}", e.ToolUseID, len(e.Content))
}

// Text returns the concatenated text of all text content blocks.
func (e *ToolResultEvent) Text() string {
	var parts []string
	for _, b := range e.Content {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "")
}

// UserEvent is emitted when the CLI feeds a message back to the model.
//
// The CLI emits these as "type":"user" JSONL events. They appear in two contexts:
//
//  1. Tool results — after any tool executes, this carries the output back to the
//     model for its next turn. Correlate with the preceding ToolUseEvent via
//     Content[].ToolUseID.
//
//  2. Subagent activity — when the Agent tool spawns a subagent, its prompt dispatch,
//     internal tool results, and final completion all appear as UserEvents with
//     ParentToolUseID set to the Agent ToolUseEvent.ID.
//
// Use ParentToolUseID to distinguish subagent events from top-level tool results:
//   - Empty: top-level tool result or user input
//   - Non-empty: belongs to the subagent spawned by that Agent tool call
//
// When AgentResult is non-nil, this event completes a subagent execution and
// contains its metadata (agent type, duration, token usage).
type UserEvent struct {
	Content         []UserContent
	ParentToolUseID string
	AgentResult     *AgentResult
	SessionID       string
	UUID            string
	Timestamp       string
	// IsReplay is true when this event is an echo of a user message sent via
	// stdin, produced by the CLI's --replay-user-messages flag. Replay events
	// confirm that the CLI has read and accepted the message.
	IsReplay bool
}

func (*UserEvent) event() {}
func (e *UserEvent) String() string {
	if e.AgentResult != nil {
		return fmt.Sprintf("UserEvent{AgentResult: %s, ParentToolUseID: %s}", e.AgentResult.AgentID, e.ParentToolUseID)
	}
	return fmt.Sprintf("UserEvent{Blocks: %d, ParentToolUseID: %s}", len(e.Content), e.ParentToolUseID)
}

// Text returns the concatenated text of all text content blocks.
func (e *UserEvent) Text() string {
	var parts []string
	for _, b := range e.Content {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "")
}

// UserContent represents a content block in a user message.
// Type is "text" for prompt/text content, or "tool_result" for tool output.
type UserContent struct {
	Type      string        // "text" or "tool_result"
	Text      string        // populated when Type == "text"
	ToolUseID string        // populated when Type == "tool_result"
	Content   []ToolContent // tool result content; populated when Type == "tool_result"
}

// AgentResult contains metadata from a completed subagent execution.
// Present on UserEvent when the event carries the final output of an Agent tool call.
type AgentResult struct {
	Status            string
	Prompt            string
	AgentID           string
	AgentType         string
	Content           []ToolContent
	TotalDurationMs   int
	TotalTokens       int
	TotalToolUseCount int
}

// RateLimitEvent is emitted when the CLI reports rate limit status changes.
// Status is "allowed", "allowed_warning" (approaching limit), or "rejected" (limit hit).
type RateLimitEvent struct {
	Status                string
	Utilization           float64
	ResetsAt              int64  // unix timestamp when rate limit window resets (0 if absent)
	RateLimitType         string // e.g. "five_hour", "seven_day", "seven_day_opus"
	OverageStatus         string // overage/pay-as-you-go status if applicable
	OverageResetsAt       int64
	OverageDisabledReason string
	UUID                  string
	SessionID             string
	Raw                   map[string]any // full raw dict for forward compat
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

// ResultEvent is emitted at the end of a session (successful or error).
type ResultEvent struct {
	Text             string
	Subtype          string
	StopReason       string
	StructuredOutput json.RawMessage
	Duration         time.Duration
	CostUSD          float64
	SessionID        string
	NumTurns         int
	Usage            Usage
	// ModelUsage contains per-model usage keyed by model ID.
	ModelUsage map[string]ModelUsage
	// ContextSnapshot captures usage from the last API call's stream events.
	// Nil if no stream_event events were observed.
	ContextSnapshot *ContextSnapshot
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

// ModelUsage contains per-model usage statistics including context window metadata.
// The result event reports one entry per model used during the session.
type ModelUsage struct {
	InputTokens       int
	OutputTokens      int
	CacheReadTokens   int
	CacheCreateTokens int
	CostUSD           float64
	ContextWindow     int
	MaxOutputTokens   int
	WebSearchRequests int
	WebFetchRequests  int
}

// ContextSnapshot captures token usage from the last API call in a streaming session.
// Populated from the last message_start + message_delta pair observed in stream_event events.
// Nil on ResultEvent when WithIncludePartialMessages is not enabled.
type ContextSnapshot struct {
	InputTokens              int
	CacheReadInputTokens     int
	CacheCreationInputTokens int
	OutputTokens             int
	ContextWindow            int
}

// ContextManagementEvent is emitted when the CLI compresses or summarizes
// older conversation turns to stay within the context window.
// Raw contains the full JSON payload for forward compatibility.
type ContextManagementEvent struct {
	Raw json.RawMessage
}

func (*ContextManagementEvent) event() {}
func (e *ContextManagementEvent) String() string {
	return fmt.Sprintf("ContextManagementEvent{len: %d}", len(e.Raw))
}

// ActivityState describes the high-level activity of a CLI session.
// Distinct from the lifecycle State (starting/idle/running/done/failed):
// it tells consumers whether silence in the event stream means the model
// is generating, a tool is executing, or the session is between turns.
type ActivityState string

const (
	// ActivityIdle means the session is between turns (no query in flight).
	ActivityIdle ActivityState = "idle"
	// ActivityThinking means the model is generating.
	ActivityThinking ActivityState = "thinking"
	// ActivityAwaitingToolResult means at least one top-level tool_use has
	// been emitted without its matching tool_result; the CLI is executing
	// the tool (or waiting for a permission callback).
	ActivityAwaitingToolResult ActivityState = "awaiting_tool_result"
)

// CLIStateChangeEvent signals a transition in activity state. Emitted
// immediately BEFORE the triggering event (e.g. the first top-level
// ToolUseEvent of a turn is preceded by a transition to
// ActivityAwaitingToolResult), so consumers can update their view of the
// session before processing the event itself.
//
// Backward compatible: callers that don't care about activity state can
// ignore it in their type switch.
type CLIStateChangeEvent struct {
	State ActivityState
	At    time.Time
}

func (*CLIStateChangeEvent) event() {}
func (e *CLIStateChangeEvent) String() string {
	return fmt.Sprintf("CLIStateChangeEvent{State: %s}", e.State)
}

// ToolProgressEvent is emitted periodically while the session is in
// ActivityAwaitingToolResult. It proves liveness in the absence of parsed
// events and carries elapsed time since the tool_use was emitted.
//
// ToolUseID / ToolName identify the first pending top-level tool_use and
// remain stable across ticks even if additional parallel tool_use calls
// are outstanding. Consumers can render "Bash running for 4m 12s" without
// computing elapsed time themselves.
type ToolProgressEvent struct {
	ToolUseID string
	ToolName  string
	Elapsed   time.Duration
	At        time.Time
}

func (*ToolProgressEvent) event() {}
func (e *ToolProgressEvent) String() string {
	return fmt.Sprintf("ToolProgressEvent{Tool: %s, ID: %s, Elapsed: %s}", e.ToolName, e.ToolUseID, e.Elapsed)
}

// ExitReason classifies why the CLI process terminated. Carried by
// CLIExitEvent so consumers can give users actionable messages and
// distinguish clean shutdowns from crashes.
type ExitReason string

const (
	// ExitReasonNormal indicates the process exited cleanly with code 0.
	ExitReasonNormal ExitReason = "normal"
	// ExitReasonKilled indicates the process was terminated by a signal
	// (SIGKILL, SIGTERM, OOM kill, etc.).
	ExitReasonKilled ExitReason = "killed"
	// ExitReasonCrashed indicates the process exited with a non-zero code
	// without being signaled (CLI bug, panic, fatal API error).
	ExitReasonCrashed ExitReason = "crashed"
	// ExitReasonContextCanceled indicates the session context was canceled
	// (Close timeout, parent ctx cancel) and the SDK terminated the process.
	ExitReasonContextCanceled ExitReason = "context_canceled"
	// ExitReasonUnknown is used when the cause cannot be classified.
	ExitReasonUnknown ExitReason = "unknown"
)

// CLIExitEvent is the last event emitted before the events channel closes.
// Describes the cause of the CLI process termination so consumers can
// distinguish a clean shutdown from a crash, signal kill, or context cancel.
//
// Backward compatible: callers that don't type-switch for CLIExitEvent
// simply ignore it.
type CLIExitEvent struct {
	Reason   ExitReason
	ExitCode int    // process exit code; -1 if not an *exec.ExitError
	Signal   string // signal name (e.g. "SIGKILL", "SIGTERM"); empty if not signaled
	Err      error  // underlying error (e.g. *Error from processExitError); nil on clean exit
	At       time.Time
}

func (*CLIExitEvent) event() {}
func (e *CLIExitEvent) String() string {
	if e.Signal != "" {
		return fmt.Sprintf("CLIExitEvent{Reason: %s, ExitCode: %d, Signal: %s}", e.Reason, e.ExitCode, e.Signal)
	}
	return fmt.Sprintf("CLIExitEvent{Reason: %s, ExitCode: %d}", e.Reason, e.ExitCode)
}

// UnknownEvent is emitted when the CLI sends an event type not recognized
// by this SDK version. Preserves the full raw JSON for inspection.
type UnknownEvent struct {
	Type string
	Raw  json.RawMessage
}

func (*UnknownEvent) event() {}
func (e *UnknownEvent) String() string {
	return fmt.Sprintf("UnknownEvent{Type: %s, len: %d}", e.Type, len(e.Raw))
}
