package claudecli

import "testing"

// Several system subtypes reuse the "message" key for a plain string rather
// than an assistant message object. rawMessage.UnmarshalJSON already tolerates
// that; this pins the behavior so a future change to it cannot silently turn
// these events into ErrorEvents about malformed JSONL.
func TestStringMessageFieldDoesNotBreakParsing(t *testing.T) {
	const line = `{"type":"system","subtype":"permission_denied","session_id":"s1","tool_name":"Bash","message":"denied by rule"}`
	for _, ev := range parseLines(t, line) {
		if e, ok := ev.(*ErrorEvent); ok {
			t.Fatalf("string message field produced ErrorEvent: %v", e.Err)
		}
	}
}

func TestConversationResetEvent(t *testing.T) {
	const line = `{"type":"conversation_reset","new_conversation_id":"11111111-2222-4333-8444-555555555555","session_id":"s1","uuid":"u1"}`
	var got *ConversationResetEvent
	for _, ev := range parseLines(t, line) {
		if e, ok := ev.(*ConversationResetEvent); ok {
			got = e
		}
	}
	if got == nil {
		t.Fatal("conversation_reset did not decode to *ConversationResetEvent")
	}
	if got.NewConversationID != "11111111-2222-4333-8444-555555555555" {
		t.Errorf("NewConversationID = %q", got.NewConversationID)
	}
}

func TestBackgroundTasksChangedEvent(t *testing.T) {
	const line = `{"type":"system","subtype":"background_tasks_changed","session_id":"s1","tasks":[{"task_id":"t1","task_type":"local_agent","description":"Explore repo"},{"task_id":"t2","task_type":"local_workflow","description":"Run spec"}]}`
	var got *BackgroundTasksChangedEvent
	for _, ev := range parseLines(t, line) {
		if e, ok := ev.(*BackgroundTasksChangedEvent); ok {
			got = e
		}
	}
	if got == nil {
		t.Fatal("background_tasks_changed did not decode")
	}
	if len(got.Tasks) != 2 {
		t.Fatalf("Tasks = %d, want 2", len(got.Tasks))
	}
	if got.Tasks[0].TaskID != "t1" || got.Tasks[1].TaskType != "local_workflow" {
		t.Errorf("Tasks decoded wrong: %+v", got.Tasks)
	}
}

// An empty set is the "nothing running" level and must decode as such, not be
// skipped — dropping it is how a stale running indicator gets wedged.
func TestBackgroundTasksChangedEmptySet(t *testing.T) {
	const line = `{"type":"system","subtype":"background_tasks_changed","session_id":"s1","tasks":[]}`
	var got *BackgroundTasksChangedEvent
	for _, ev := range parseLines(t, line) {
		if e, ok := ev.(*BackgroundTasksChangedEvent); ok {
			got = e
		}
	}
	if got == nil {
		t.Fatal("empty background_tasks_changed did not decode")
	}
	if len(got.Tasks) != 0 {
		t.Errorf("Tasks = %d, want 0", len(got.Tasks))
	}
}

func TestSessionStateChangedEvent(t *testing.T) {
	const line = `{"type":"system","subtype":"session_state_changed","session_id":"s1","state":"requires_action"}`
	var got *SessionStateChangedEvent
	for _, ev := range parseLines(t, line) {
		if e, ok := ev.(*SessionStateChangedEvent); ok {
			got = e
		}
	}
	if got == nil {
		t.Fatal("session_state_changed did not decode")
	}
	if got.State != SessionStateRequiresAction {
		t.Errorf("State = %q, want %q", got.State, SessionStateRequiresAction)
	}
}

func TestPermissionDeniedEvent(t *testing.T) {
	const line = `{"type":"system","subtype":"permission_denied","session_id":"s1","tool_name":"Bash","tool_use_id":"toolu_1","agent_id":"agent_7","decision_reason_type":"rule","decision_reason":"matched deny rule","message":"Permission denied"}`
	var got *PermissionDeniedEvent
	for _, ev := range parseLines(t, line) {
		if e, ok := ev.(*PermissionDeniedEvent); ok {
			got = e
		}
	}
	if got == nil {
		t.Fatal("permission_denied did not decode")
	}
	if got.ToolName != "Bash" || got.ToolUseID != "toolu_1" || got.AgentID != "agent_7" {
		t.Errorf("identity fields wrong: %+v", got)
	}
	if got.DecisionReasonType != "rule" || got.Message != "Permission denied" {
		t.Errorf("reason fields wrong: %+v", got)
	}
}

// keep_alive is a heartbeat the protocol requires receivers to ignore.
// Surfacing it would read as a parse failure to consumers.
func TestKeepAliveIsIgnored(t *testing.T) {
	const heartbeat = `{"type":"keep_alive"}`
	const real = `{"type":"system","subtype":"session_state_changed","session_id":"s1","state":"idle"}`
	for _, ev := range parseLines(t, heartbeat, real) {
		switch e := ev.(type) {
		case *UnknownEvent:
			t.Errorf("keep_alive surfaced as UnknownEvent{Type: %q}", e.Type)
		case *ErrorEvent:
			t.Errorf("keep_alive surfaced as ErrorEvent: %v", e.Err)
		}
	}
}

// Observed live: ReloadSkills triggers commands_changed, which previously
// surfaced as *UnknownEvent.
func TestCommandsChangedEvent(t *testing.T) {
	const line = `{"type":"system","subtype":"commands_changed","session_id":"s1","commands":[{"name":"code-review","description":"Review changes","argumentHint":"since"},{"name":"tdd","description":"Test first"}]}`
	var got *CommandsChangedEvent
	for _, ev := range parseLines(t, line) {
		if e, ok := ev.(*CommandsChangedEvent); ok {
			got = e
		}
	}
	if got == nil {
		t.Fatal("commands_changed did not decode")
	}
	if len(got.Commands) != 2 || got.Commands[0].Name != "code-review" {
		t.Errorf("Commands = %+v", got.Commands)
	}
	if got.Commands[0].ArgumentHint != "since" {
		t.Errorf("ArgumentHint = %q", got.Commands[0].ArgumentHint)
	}
}
