package claudecli

import "time"

// activityTracker derives CLIStateChangeEvent transitions from a stream of
// events. Not goroutine-safe; callers must serialize observe/markQuery calls.
//
// State machine:
//   - idle → thinking: first top-level content event (TextEvent, ThinkingEvent,
//     ToolUseEvent, TurnEvent) of a turn, or explicit markQuery() when the
//     user sends a message.
//   - thinking → awaiting_tool_result: top-level ToolUseEvent emitted.
//   - awaiting_tool_result → awaiting_tool_result: additional top-level
//     ToolUseEvent (pending count increments, no event emitted).
//   - awaiting_tool_result → thinking: last top-level ToolResultEvent
//     (pending count hits zero).
//   - any → idle: ResultEvent or fatal ErrorEvent. An unsolicited result
//     (the CLI's own task-notification turn) goes to thinking instead while a
//     query is still pending.
//
// Subagent events (ParentToolUseID != "") do not affect top-level state —
// from the consumer's perspective, the parent Agent tool_use is still
// "awaiting its result" regardless of what happens inside.
type activityTracker struct {
	state           ActivityState
	pendingToolUses int
	now             func() time.Time
	// firstPending captures the first top-level tool_use observed since
	// entering ActivityAwaitingToolResult. Cleared on transition out so
	// ToolProgressEvent ticks report stable values across parallel tool_use
	// calls and reset cleanly on the next tool-using turn.
	firstPending pendingToolUseInfo
}

// pendingToolUseInfo identifies the first outstanding top-level tool_use
// for ToolProgressEvent emission.
type pendingToolUseInfo struct {
	ID        string
	Name      string
	StartedAt time.Time
}

func newActivityTracker() *activityTracker {
	return &activityTracker{state: ActivityIdle, now: time.Now}
}

// observe returns a CLIStateChangeEvent to emit BEFORE ev, or nil if ev
// does not trigger a state transition.
func (t *activityTracker) observe(ev Event) *CLIStateChangeEvent {
	next := t.state
	switch e := ev.(type) {
	case *TurnEvent:
		if t.state == ActivityIdle {
			next = ActivityThinking
		}
	case *TextEvent:
		if e.ParentToolUseID == "" && t.state == ActivityIdle {
			next = ActivityThinking
		}
	case *ThinkingEvent:
		if e.ParentToolUseID == "" && t.state == ActivityIdle {
			next = ActivityThinking
		}
	case *ToolUseEvent:
		if e.ParentToolUseID == "" {
			if t.pendingToolUses == 0 {
				t.firstPending = pendingToolUseInfo{
					ID:        e.ID,
					Name:      e.Name,
					StartedAt: t.now(),
				}
			}
			t.pendingToolUses++
			next = ActivityAwaitingToolResult
		}
	case *ToolResultEvent:
		if e.ParentToolUseID == "" {
			if t.pendingToolUses > 0 {
				t.pendingToolUses--
			}
			if t.pendingToolUses == 0 {
				next = ActivityThinking
			}
		}
	case *UserEvent:
		// Top-level tool_result blocks arrive inside user events too (CLI
		// feeds tool output back to the model). Count tool_result blocks
		// so pairing stays balanced when tool_use came via an assistant
		// message and the result comes via a user message.
		if e.ParentToolUseID == "" {
			for _, b := range e.Content {
				if b.Type == "tool_result" && t.pendingToolUses > 0 {
					t.pendingToolUses--
				}
			}
			if t.pendingToolUses == 0 && t.state == ActivityAwaitingToolResult {
				next = ActivityThinking
			}
		}
	case *ResultEvent:
		t.pendingToolUses = 0
		next = ActivityIdle
	case *ErrorEvent:
		if e.Fatal {
			t.pendingToolUses = 0
			next = ActivityIdle
		}
	}
	return t.moveTo(next)
}

// observeUnsolicitedResult handles a result that closes a turn the CLI started
// by itself (see Session.observeUnsolicitedResult). The turn's tool uses are
// over. The tracker goes idle unless a caller's prompt is still waiting, in
// which case the CLI is about to start on it and the tracker stays thinking.
func (t *activityTracker) observeUnsolicitedResult(queryPending bool) *CLIStateChangeEvent {
	t.pendingToolUses = 0
	if queryPending {
		return t.moveTo(ActivityThinking)
	}
	return t.moveTo(ActivityIdle)
}

// moveTo transitions to next and returns the event to emit, or nil when the
// tracker is already there.
func (t *activityTracker) moveTo(next ActivityState) *CLIStateChangeEvent {
	if next == t.state {
		return nil
	}
	t.state = next
	if next != ActivityAwaitingToolResult {
		t.firstPending = pendingToolUseInfo{}
	}
	return &CLIStateChangeEvent{State: next, At: t.now()}
}

// FirstPending returns the first outstanding top-level tool_use, or ok=false
// when the tracker is not in ActivityAwaitingToolResult. Callers must hold
// the same lock used to serialize observe/markQuery.
func (t *activityTracker) FirstPending() (pendingToolUseInfo, bool) {
	if t.state != ActivityAwaitingToolResult || t.firstPending.ID == "" {
		return pendingToolUseInfo{}, false
	}
	return t.firstPending, true
}

// markQuery returns a CLIStateChangeEvent to ActivityThinking if the
// tracker is currently idle. Used by Session on Query/SendMessage so the
// transition is visible before the CLI emits anything.
func (t *activityTracker) markQuery() *CLIStateChangeEvent {
	if t.state != ActivityIdle {
		return nil
	}
	t.state = ActivityThinking
	return &CLIStateChangeEvent{State: ActivityThinking, At: t.now()}
}

// State returns the current activity state.
func (t *activityTracker) State() ActivityState {
	return t.state
}
