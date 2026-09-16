package claudecli

import (
	"encoding/json"
	"fmt"
	"slices"
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

	// MCPServerErrors lists --mcp-config entries the CLI skipped because
	// their configuration failed validation. These servers never start and
	// never appear in MCPServers, so an empty MCPServers list plus a
	// populated MCPServerErrors means the config was rejected rather than
	// the servers having failed at runtime. Requires CLI 2.1.219+.
	MCPServerErrors []MCPServerError

	// CLIVersion is the Claude CLI version running this session.
	CLIVersion string
	// CWD is the working directory the CLI resolved for the session.
	CWD string
	// PermissionMode is the mode the session actually started in, which may
	// differ from a requested mode the CLI declined to honor.
	PermissionMode PermissionMode
	// OutputStyle is the configured output style, e.g. "default".
	OutputStyle string
	// SlashCommands lists the skills and commands available in the session.
	SlashCommands []string
	// Plugins lists the plugins loaded for the session.
	Plugins []PluginInfo

	// Capabilities lists the optional protocol features this CLI supports,
	// e.g. "interrupt_receipt_v1", "interrupt_cancel_queued_v1",
	// "msg_lifecycle_v1". This is the CLI's own feature-negotiation channel:
	// gate optional behavior on HasCapability rather than comparing
	// CLIVersion strings. Older CLIs omit it, so an empty slice means "no
	// advertisement", not "no features".
	Capabilities []string
}

// HasCapability reports whether the CLI advertised the named optional protocol
// feature on its init event.
func (e *InitEvent) HasCapability(name string) bool {
	return slices.Contains(e.Capabilities, name)
}

// Capability tokens advertised on InitEvent.Capabilities. The CLI may advertise
// others; these are the ones this package's behavior depends on.
const (
	// CapabilityInterruptReceipt means an interrupt response carries the
	// still_queued receipt (see Session.InterruptWithQueued).
	CapabilityInterruptReceipt = "interrupt_receipt_v1"
	// CapabilityInterruptCancelQueued means an interrupt request honors the
	// cancel_queued field. Older CLIs ignore it and behave as if false.
	CapabilityInterruptCancelQueued = "interrupt_cancel_queued_v1"
)

// PluginInfo identifies a plugin loaded into the session.
type PluginInfo struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func (p PluginInfo) String() string { return p.Name }

// MCPServerError describes an MCP server the CLI refused to start because its
// configuration was invalid.
type MCPServerError struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (e MCPServerError) String() string {
	return fmt.Sprintf("%s (%s): %s", e.Name, e.Type, e.Message)
}

func (*InitEvent) event() {}
func (e *InitEvent) String() string {
	return fmt.Sprintf("InitEvent{SessionID: %s, Model: %s}", e.SessionID, e.Model)
}

// ModelDisplayName returns the human-readable name of the session model, e.g.
// "Opus 4.8". It is shorthand for ModelDisplayName(e.Model).
func (e *InitEvent) ModelDisplayName() string {
	return ModelDisplayName(e.Model)
}

// PromptSuggestionEvent carries a predicted next user prompt, emitted after
// each turn when the run enabled WithPromptSuggestions. It is advisory: the
// suggestion is a guess at what the user might ask next, not an instruction
// and not something the model committed to.
type PromptSuggestionEvent struct {
	Suggestion string
	SessionID  string
	UUID       string
}

func (*PromptSuggestionEvent) event() {}
func (e *PromptSuggestionEvent) String() string {
	return fmt.Sprintf("PromptSuggestionEvent{%q}", e.Suggestion)
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
// "task_started", "task_progress", "task_updated", "task_notification").
//
// ToolUseID links to the parent Agent ToolUseEvent.ID that spawned this task.
// TaskID is a unique identifier for the subagent task instance.
//
// Subtype meanings:
//   - "task_started": subagent spawned. Description, TaskType, Prompt are set.
//   - "task_progress": subagent working. Usage fields update, LastToolName shows current tool.
//   - "task_updated": lightweight status patch (Status, EndTime).
//   - "task_notification": subagent finished. Status ("completed"), Summary, final Usage.
//
// Dynamic workflows (https://code.claude.com/docs/en/workflows) surface
// through this same machinery as a single synthetic task with
// TaskType == "local_workflow" (see IsWorkflow). For those, WorkflowName
// is set, Prompt carries the full workflow script (on task_started), and
// OutputFile points at the workflow's result file (on a completed
// task_notification).
//
// A workflow's task_progress comes in two kinds (CLI 2.1.270):
//
//   - Tree ticks carry WorkflowProgress, the full phase and agent list with
//     per-agent state. The CLI sends one only when an agent changes state or
//     a phase changes.
//   - Usage ticks have WorkflowProgress == nil. They are the majority, sent as
//     agents make tool calls. They carry no tree, so nil means "no tree in
//     this tick", never "no agents". Use HasWorkflowTree to tell them apart.
//
// On both kinds Description names the agent that ticked ("<phase>: <label>",
// or the bare label without phases), and the CLI puts that label, not a
// tool, in LastToolName. The SDK resolves it into WorkflowAgentLabel,
// WorkflowPhaseTitle and WorkflowAgentID. TotalTokens and ToolUses are
// summed over the whole workflow, not the named agent. The terminal
// task_notification carries no tree.
//
// Workflow agents' own messages never reach the stream. To follow one live,
// read its transcript with ReadWorkflowAgentTranscript, using the
// UserEvent.WorkflowLaunch of the run and the agent id from the tree or from
// ReadWorkflowJournal.
type TaskEvent struct {
	Subtype   string // "task_started", "task_progress", "task_updated", "task_notification"
	TaskID    string
	ToolUseID string // parent Agent ToolUseEvent.ID
	SessionID string

	// task_started
	Description string
	// SubagentType is the agent type for Task-tool subagents, e.g. "Explore".
	// Sent on task_started and task_progress; empty for workflow tasks.
	SubagentType string
	// TaskType classifies the task, e.g. "local_agent" or "local_workflow".
	// The CLI sends it only on task_started; the SDK backfills it onto the
	// same task's later events (see IsWorkflow).
	TaskType string
	Prompt   string
	// WorkflowName is set when TaskType == "local_workflow". Like TaskType it
	// is backfilled onto the task's later events from its task_started.
	WorkflowName string

	// task_progress
	// LastToolName is the tool a subagent last called. On workflow ticks the
	// CLI sends the ticking agent's label here instead; prefer
	// WorkflowAgentLabel.
	LastToolName string
	// WorkflowProgress carries per-phase and per-agent state on a workflow
	// tree tick. It is nil on usage ticks, which carry no tree (see
	// HasWorkflowTree), and on ordinary subagent tasks.
	WorkflowProgress []WorkflowProgressEntry
	// WorkflowAgentLabel, WorkflowPhaseTitle and WorkflowAgentID identify the
	// agent a workflow task_progress tick is about, on both tree and usage
	// ticks. The SDK resolves them by matching Description against the agents
	// of the latest tree for the task. They are empty when the description
	// matches no known agent or is ambiguous; WorkflowPhaseTitle is also
	// empty for an agent without a phase, and WorkflowAgentID for an agent
	// that is still queued.
	WorkflowAgentLabel string
	WorkflowPhaseTitle string
	WorkflowAgentID    string

	// task_notification
	Status  string
	Summary string
	// OutputFile is the path to the workflow's result file, set on a
	// completed workflow task_notification. Empty otherwise.
	OutputFile string

	// task_updated
	EndTime int64 // patch.end_time, epoch milliseconds; 0 if absent

	// OwnedBySubagent is true when a subagent, not the main loop, owns the
	// task. Bash commands run inside workflow agents surface on the parent
	// stream this way, as task_started/task_notification with TaskType
	// "local_bash"; their ToolUseID is the Bash tool_use id in the owning
	// agent's transcript, not a main-loop tool call. The CLI sends
	// owned_by_subagent only on task_started; the SDK backfills it onto the
	// task's later events like TaskType.
	OwnedBySubagent bool
	// IsBackgrounded reports the CLI's is_backgrounded flag, sent on the
	// task_started of shell tasks and not backfilled. A foreground Bash call
	// inside a workflow agent reports false. False when absent.
	IsBackgrounded bool

	// task_progress + task_notification
	TotalTokens int
	ToolUses    int
	DurationMs  int

	// Raw contains the full JSON line for forward compatibility.
	Raw json.RawMessage
}

func (*TaskEvent) event() {}
func (e *TaskEvent) String() string {
	if e.IsWorkflow() {
		return fmt.Sprintf("TaskEvent{Subtype: %s, Workflow: %s, TaskID: %s}", e.Subtype, e.WorkflowName, e.TaskID)
	}
	return fmt.Sprintf("TaskEvent{Subtype: %s, TaskID: %s, ToolUseID: %s}", e.Subtype, e.TaskID, e.ToolUseID)
}

// IsWorkflow reports whether this task is a dynamic workflow run
// (TaskType == "local_workflow") rather than an ordinary subagent.
//
// The CLI stamps task_type only on task_started; later task_progress,
// task_updated, and task_notification events for the same task_id omit it.
// The SDK backfills TaskType (and WorkflowName) onto those events from the
// task_started of the same task_id, so IsWorkflow stays correct across the
// whole lifecycle — including the terminal task_notification.
func (e *TaskEvent) IsWorkflow() bool { return e.TaskType == "local_workflow" }

// HasWorkflowTree reports whether this event carries a workflow progress tree
// (WorkflowProgress). A workflow task_progress without one is a usage tick:
// it still names the ticking agent and updates the workflow's usage totals,
// but says nothing about the agent list, so keep the last tree you saw.
func (e *TaskEvent) HasWorkflowTree() bool { return e.WorkflowProgress != nil }

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

// ThinkingEvent contains the model's thinking output.
//
// Content may be empty while Signature is set: the CLI can emit a thinking
// block whose text is withheld but whose signature is present. Treat
// Content=="" with a non-empty Signature as "thinking hidden", not "no
// thinking occurred".
//
// ParentToolUseID is set when this event comes from a subagent (links to the
// parent Agent ToolUseEvent.ID). Empty for top-level assistant turns.
//
// Model, SubagentType and TaskDescription describe the assistant message this
// block came from. On subagent events Model is the resolved API model id the
// subagent actually ran on (e.g. "claude-haiku-4-5-20251001") — the only place
// it appears on the wire, and more specific than the alias in the spawning
// Agent tool's input, which is absent entirely when the subagent inherits the
// parent's model. All three require WithForwardSubagentText(): without it the
// CLI emits no subagent assistant messages at all, so they stay empty.
type ThinkingEvent struct {
	Content         string
	Signature       string
	ParentToolUseID string
	Model           string
	SubagentType    string
	TaskDescription string
}

func (*ThinkingEvent) event() {}
func (e *ThinkingEvent) String() string {
	return fmt.Sprintf("ThinkingEvent{len: %d}", len(e.Content))
}

// TextEvent contains assistant text output.
// ParentToolUseID is set when this event comes from a subagent (links to the
// parent Agent ToolUseEvent.ID). Empty for top-level assistant turns.
//
// Model, SubagentType and TaskDescription describe the assistant message this
// block came from. On subagent events Model is the resolved API model id the
// subagent actually ran on (e.g. "claude-haiku-4-5-20251001") — the only place
// it appears on the wire, and more specific than the alias in the spawning
// Agent tool's input, which is absent entirely when the subagent inherits the
// parent's model. All three require WithForwardSubagentText(): without it the
// CLI emits no subagent assistant messages at all, so they stay empty.
type TextEvent struct {
	Content         string
	ParentToolUseID string
	Model           string
	SubagentType    string
	TaskDescription string
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
//
// ServerSide is true for server_tool_use blocks (web search, code execution)
// and MCP is true for mcp_tool_use blocks. Both carry the same ID/Name/Input
// shape as regular tool_use.
//
// Model, SubagentType and TaskDescription describe the assistant message this
// block came from. On subagent events Model is the resolved API model id the
// subagent actually ran on (e.g. "claude-haiku-4-5-20251001") — the only place
// it appears on the wire, and more specific than the alias in the spawning
// Agent tool's input, which is absent entirely when the subagent inherits the
// parent's model. All three require WithForwardSubagentText(): without it the
// CLI emits no subagent assistant messages at all, so they stay empty.
type ToolUseEvent struct {
	ID              string
	Name            string
	Input           json.RawMessage
	ParentToolUseID string
	Model           string
	SubagentType    string
	TaskDescription string
	ServerSide      bool
	MCP             bool
}

func (*ToolUseEvent) event() {}
func (e *ToolUseEvent) String() string {
	switch {
	case e.ServerSide:
		return fmt.Sprintf("ToolUseEvent{Name: %s, ID: %s, ServerSide}", e.Name, e.ID)
	case e.MCP:
		return fmt.Sprintf("ToolUseEvent{Name: %s, ID: %s, MCP}", e.Name, e.ID)
	default:
		return fmt.Sprintf("ToolUseEvent{Name: %s, ID: %s}", e.Name, e.ID)
	}
}

// ConversationResetEvent is emitted when the conversation is reset — by
// /clear, by leaving plan mode, or by a fresh-session flow.
//
// This is a transcript boundary, not a session restart: the CLI process and
// session id are unchanged, but everything before it is gone from the model's
// context. Consumers holding a transcript must start a fresh one under
// NewConversationID and drop any cached session title; continuing to append to
// the old transcript silently diverges from what the model actually sees.
type ConversationResetEvent struct {
	NewConversationID string
	SessionID         string
	UUID              string
}

func (*ConversationResetEvent) event() {}
func (e *ConversationResetEvent) String() string {
	return fmt.Sprintf("ConversationResetEvent{NewConversationID: %s}", e.NewConversationID)
}

// BackgroundTask identifies one live background task.
type BackgroundTask struct {
	TaskID      string `json:"task_id"`
	TaskType    string `json:"task_type"`
	Description string `json:"description"`
}

// BackgroundTasksChangedEvent reports the complete set of live background
// tasks after a membership change (start, completion, kill, or a foreground
// agent being backgrounded).
//
// REPLACE semantics: swap your set for Tasks rather than applying a delta.
// This is a *level* signal, unlike the task_started/task_notification edge
// pair — a consumer that only needs "is background work running" should read
// it here, because a missed edge otherwise wedges a stale running indicator
// forever. The payload carries ids only; do not try to correlate it with the
// edge stream, whose relative ordering is unspecified.
//
// The level is per-process: nothing is emitted at startup, so reset to the
// empty set whenever the CLI process restarts and let the next change
// repopulate it.
type BackgroundTasksChangedEvent struct {
	Tasks     []BackgroundTask
	SessionID string
	UUID      string
}

func (*BackgroundTasksChangedEvent) event() {}
func (e *BackgroundTasksChangedEvent) String() string {
	return fmt.Sprintf("BackgroundTasksChangedEvent{Tasks: %d}", len(e.Tasks))
}

// Session states reported by SessionStateChangedEvent.
const (
	SessionStateIdle           = "idle"
	SessionStateRunning        = "running"
	SessionStateRequiresAction = "requires_action"
)

// SessionStateChangedEvent reports the CLI's own view of session state, one of
// SessionStateIdle, SessionStateRunning, or SessionStateRequiresAction.
//
// More reliable than inferring state from result/assistant traffic, and the
// only signal that distinguishes "waiting on the user" from "idle".
type SessionStateChangedEvent struct {
	State     string
	SessionID string
	UUID      string
}

func (*SessionStateChangedEvent) event() {}
func (e *SessionStateChangedEvent) String() string {
	return fmt.Sprintf("SessionStateChangedEvent{State: %s}", e.State)
}

// PermissionDeniedEvent reports a tool call denied without an interactive
// prompt — by the auto-mode classifier, dontAsk mode, a deny rule, or
// headless auto-deny. With a permission callback registered, the "ask" path
// arrives as a ToolPermissionRequest instead and this covers only the deny
// short-circuit; without one, "ask" decisions are terminal and also land here.
//
// Advisory and best-effort: in rare races a denial can occur without an event.
// ResultEvent's permission_denials is the authoritative record.
//
// AgentID is set when the denied call originated inside a subagent.
type PermissionDeniedEvent struct {
	ToolName           string
	ToolUseID          string
	AgentID            string
	DecisionReasonType string
	DecisionReason     string
	Message            string
	SessionID          string
	UUID               string
}

func (*PermissionDeniedEvent) event() {}
func (e *PermissionDeniedEvent) String() string {
	return fmt.Sprintf("PermissionDeniedEvent{Tool: %s, Reason: %s}", e.ToolName, e.DecisionReasonType)
}

// CommandsChangedEvent carries the full slash-command list after a mid-session
// change — skills discovered as the agent works in a new subdirectory, or an
// explicit Session.ReloadSkills/ReloadPlugins.
//
// REPLACE semantics: swap your cached command list for Commands.
type CommandsChangedEvent struct {
	Commands  []SlashCommand
	SessionID string
	UUID      string
}

func (*CommandsChangedEvent) event() {}
func (e *CommandsChangedEvent) String() string {
	return fmt.Sprintf("CommandsChangedEvent{Commands: %d}", len(e.Commands))
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
//
// When WorkflowLaunch is non-nil, this event reports that a dynamic
// workflow was launched in the background (tool_use_result
// status "async_launched"). Use it to monitor the run out-of-band — see
// WatchWorkflow and ReadWorkflowSnapshot.
type UserEvent struct {
	Content         []UserContent
	ParentToolUseID string
	AgentResult     *AgentResult
	WorkflowLaunch  *WorkflowLaunch
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
	switch {
	case e.WorkflowLaunch != nil:
		return fmt.Sprintf("UserEvent{WorkflowLaunch: %s, RunID: %s}", e.WorkflowLaunch.WorkflowName, e.WorkflowLaunch.RunID)
	case e.AgentResult != nil:
		return fmt.Sprintf("UserEvent{AgentResult: %s, ParentToolUseID: %s}", e.AgentResult.AgentID, e.ParentToolUseID)
	default:
		return fmt.Sprintf("UserEvent{Blocks: %d, ParentToolUseID: %s}", len(e.Content), e.ParentToolUseID)
	}
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

// TotalTokens returns the sum of every token field — input, output, cache
// read and cache create. This is the headline "tokens used" figure for a run;
// pair it with ResultEvent.CostUSD (or ModelUsage.CostUSD) to report cost.
func (u Usage) TotalTokens() int {
	return u.InputTokens + u.OutputTokens + u.CacheReadTokens + u.CacheCreateTokens
}

func (u Usage) String() string {
	return fmt.Sprintf("Usage{in: %d, out: %d, cacheRead: %d, cacheCreate: %d, total: %d}",
		u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheCreateTokens, u.TotalTokens())
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

// TotalTokens returns the sum of every token field for this model.
func (m ModelUsage) TotalTokens() int {
	return m.InputTokens + m.OutputTokens + m.CacheReadTokens + m.CacheCreateTokens
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

// Used reports the tokens the measured API call occupied in the window:
// the prompt (fresh input plus both cache buckets) plus what the model
// generated. This is the numerator a context meter wants against
// ContextWindow.
func (c ContextSnapshot) Used() int {
	return c.InputTokens + c.CacheReadInputTokens + c.CacheCreationInputTokens + c.OutputTokens
}

// ContextSnapshotPhase says which of an API call's two usage reports produced
// a ContextSnapshotEvent.
type ContextSnapshotPhase string

const (
	// ContextSnapshotStart marks the measurement taken from message_start.
	// The prompt has been sent, so the input and cache buckets are final for
	// this call, but OutputTokens is still zero: generation has not produced
	// anything yet. A meter should render this immediately — the prompt is
	// what dominates window occupancy — and treat the output as pending
	// rather than as a genuine zero.
	ContextSnapshotStart ContextSnapshotPhase = "start"
	// ContextSnapshotFinal marks the measurement taken from message_delta,
	// the API's last usage report for a call. OutputTokens is now the final
	// count and the whole snapshot is settled.
	ContextSnapshotFinal ContextSnapshotPhase = "final"
)

// ContextSnapshotEvent reports the running context measurement while a turn is
// still in flight, so a context meter can move during the turn instead of
// jumping at the end. It carries the same numbers ResultEvent.ContextSnapshot
// reports at turn end; that field is unchanged and still authoritative for the
// final state of a turn.
//
// Cadence: two events per API call, and a turn makes one API call per model
// response — so a tool-using turn produces several pairs. Phase says which is
// which: ContextSnapshotStart from message_start (prompt final, output zero),
// ContextSnapshotFinal from message_delta (output settled). The API emits
// exactly one message_delta per message, so this is not a per-chunk firehose.
//
// Requires WithIncludePartialMessages. The measurement is decoded from inner
// stream events, which the CLI only emits under that option, and it is off by
// default — without it this event never fires, so code that blocks waiting for
// one waits forever.
//
// ContextWindow is always non-zero. The CLI does not disclose the window
// mid-turn: it appears only in the result event's ModelUsage (and in the
// get_context_usage control request). Rather than emit a measurement against a
// zero window — which renders as a full or an empty meter, both wrong — the
// event is withheld until the window for the model is known, and the window is
// then remembered for the rest of the session. In practice that means:
//
//   - The first turn of a Session emits nothing; every later turn emits, because
//     the first ResultEvent disclosed the window. Calling
//     Session.QueryContextUsage once before or during the first turn teaches the
//     window early and unblocks it.
//   - ParseEvents emits nothing, since it returns at the terminal result event
//     and so never observes a window while stream events are still arriving.
type ContextSnapshotEvent struct {
	InputTokens              int
	CacheReadInputTokens     int
	CacheCreationInputTokens int
	OutputTokens             int
	ContextWindow            int
	// Model is the model named on the message_start that opened this API
	// call, as the inner stream event reports it — bare, without the
	// context-window suffix ModelUsage keys can carry.
	Model string
	// SessionID is the session the stream event belonged to.
	SessionID string
	Phase     ContextSnapshotPhase
}

func (*ContextSnapshotEvent) event() {}

// Used reports the tokens this call occupies in the window. See
// ContextSnapshot.Used.
func (e *ContextSnapshotEvent) Used() int {
	return e.InputTokens + e.CacheReadInputTokens + e.CacheCreationInputTokens + e.OutputTokens
}

func (e *ContextSnapshotEvent) String() string {
	return fmt.Sprintf("ContextSnapshotEvent{%s: %d/%d, model: %s}", e.Phase, e.Used(), e.ContextWindow, e.Model)
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

// ThinkingTokensEvent is emitted by the CLI as a running estimate of the
// model's thinking-token usage during a turn (system subtype
// "thinking_tokens"). EstimatedTokens is the cumulative estimate and
// EstimatedTokensDelta is the increment since the previous tick. It is a
// progress/telemetry signal, not authoritative accounting — use
// ResultEvent.Usage for final token counts. Appears in ordinary sessions,
// not only during workflows.
type ThinkingTokensEvent struct {
	EstimatedTokens      int
	EstimatedTokensDelta int
	SessionID            string
	UUID                 string
}

func (*ThinkingTokensEvent) event() {}
func (e *ThinkingTokensEvent) String() string {
	return fmt.Sprintf("ThinkingTokensEvent{Estimated: %d (+%d)}", e.EstimatedTokens, e.EstimatedTokensDelta)
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

// CLIToolProgressEvent is emitted by the CLI (not the SDK) when a tool
// execution is in progress. Unlike the synthetic ToolProgressEvent (emitted
// by Session's ticker), this comes directly from the CLI's JSONL stream
// as a top-level "tool_progress" event.
type CLIToolProgressEvent struct {
	ToolUseID      string
	ToolName       string
	ElapsedSeconds float64
	TaskID         string
}

func (*CLIToolProgressEvent) event() {}
func (e *CLIToolProgressEvent) String() string {
	return fmt.Sprintf("CLIToolProgressEvent{Tool: %s, ID: %s, Elapsed: %.1fs}", e.ToolName, e.ToolUseID, e.ElapsedSeconds)
}

// ToolUseSummaryEvent is emitted after tool execution with a summary of what
// the tool did. PrecedingToolUseIDs lists the tool_use IDs that this summary
// covers.
type ToolUseSummaryEvent struct {
	Summary             string
	PrecedingToolUseIDs []string
}

func (*ToolUseSummaryEvent) event() {}
func (e *ToolUseSummaryEvent) String() string {
	s := e.Summary
	if len(s) > 60 {
		s = s[:60] + "..."
	}
	return fmt.Sprintf("ToolUseSummaryEvent{Summary: %q, IDs: %d}", s, len(e.PrecedingToolUseIDs))
}

// AuthStatusEvent is emitted when the CLI reports authentication status
// changes during a session (e.g. token refresh).
type AuthStatusEvent struct {
	IsAuthenticating bool
	Output           string
	Error            string
}

func (*AuthStatusEvent) event() {}
func (e *AuthStatusEvent) String() string {
	return fmt.Sprintf("AuthStatusEvent{IsAuthenticating: %v}", e.IsAuthenticating)
}

// FilesPersistedEvent is emitted when the CLI confirms file persistence.
// Files lists successfully persisted files; Failed lists files that failed.
type FilesPersistedEvent struct {
	Files  []PersistedFile
	Failed []FailedFile
}

// PersistedFile describes a successfully persisted file.
type PersistedFile struct {
	Filename string
	FileID   string
}

// FailedFile describes a file that failed to persist.
type FailedFile struct {
	Filename string
	Error    string
}

func (*FilesPersistedEvent) event() {}
func (e *FilesPersistedEvent) String() string {
	return fmt.Sprintf("FilesPersistedEvent{Files: %d, Failed: %d}", len(e.Files), len(e.Failed))
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
