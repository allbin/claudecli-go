package claudecli

import "testing"

// Wire shapes below are verbatim from a live capture against Claude CLI
// 2.1.235 run with --forward-subagent-text (prompt: spawn an Explore subagent
// with model haiku). Note the Agent tool input carries the alias "haiku" while
// the subagent's own assistant message carries the resolved
// "claude-haiku-4-5-20251001" — that resolution is why the assistant message
// is the authoritative source for a subagent's model.
const subagentTaskStarted = `{"type":"system","subtype":"task_started","task_id":"a42ed3cc731261c2c","tool_use_id":"toolu_01JERPk9VitoHXcysvhY4t26","description":"Compute 2+2","subagent_type":"Explore","task_type":"local_agent","session_id":"s1","uuid":"u1"}`

const subagentAssistant = `{"type":"assistant","parent_tool_use_id":"toolu_01JERPk9VitoHXcysvhY4t26","subagent_type":"Explore","task_description":"Compute 2+2","session_id":"s1","uuid":"u2","message":{"id":"msg_011CeFMi7XqAVma2xsM3yfkv","model":"claude-haiku-4-5-20251001","role":"assistant","content":[{"type":"text","text":"4"}]}}`

const parentAssistant = `{"type":"assistant","parent_tool_use_id":null,"session_id":"s1","uuid":"u3","message":{"id":"msg_parent","model":"claude-opus-4-6","role":"assistant","content":[{"type":"text","text":"done"}]}}`

// The whole point of the subagent metadata: given a TaskEvent and a TextEvent,
// a consumer can say "task a42ed... ran on claude-haiku-4-5-20251001" without
// guessing from the Agent tool input.
func TestSubagentModelCorrelatesToTask(t *testing.T) {
	events := parseLines(t, subagentTaskStarted, subagentAssistant)

	var task *TaskEvent
	var text *TextEvent
	for _, ev := range events {
		switch e := ev.(type) {
		case *TaskEvent:
			task = e
		case *TextEvent:
			text = e
		}
	}

	if task == nil {
		t.Fatal("no TaskEvent emitted")
	}
	if text == nil {
		t.Fatal("no TextEvent emitted")
	}
	if task.SubagentType != "Explore" {
		t.Errorf("TaskEvent.SubagentType = %q, want %q", task.SubagentType, "Explore")
	}
	// The correlation key: the Task tool_use id is the subagent's parent.
	if text.ParentToolUseID != task.ToolUseID {
		t.Fatalf("ParentToolUseID %q does not match TaskEvent.ToolUseID %q",
			text.ParentToolUseID, task.ToolUseID)
	}
	if text.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("TextEvent.Model = %q, want resolved subagent model", text.Model)
	}
	if text.SubagentType != "Explore" {
		t.Errorf("TextEvent.SubagentType = %q, want %q", text.SubagentType, "Explore")
	}
	if text.TaskDescription != "Compute 2+2" {
		t.Errorf("TextEvent.TaskDescription = %q, want %q", text.TaskDescription, "Compute 2+2")
	}
}

// Top-level turns carry the main model and no subagent identity, so a consumer
// can tell parent traffic from subagent traffic by SubagentType alone.
func TestParentAssistantCarriesModelWithoutSubagentIdentity(t *testing.T) {
	var text *TextEvent
	for _, ev := range parseLines(t, parentAssistant) {
		if e, ok := ev.(*TextEvent); ok {
			text = e
		}
	}
	if text == nil {
		t.Fatal("no TextEvent emitted")
	}
	if text.Model != "claude-opus-4-6" {
		t.Errorf("Model = %q, want %q", text.Model, "claude-opus-4-6")
	}
	if text.ParentToolUseID != "" || text.SubagentType != "" || text.TaskDescription != "" {
		t.Errorf("top-level turn carries subagent identity: parent=%q type=%q desc=%q",
			text.ParentToolUseID, text.SubagentType, text.TaskDescription)
	}
}

// Both decode loops must agree; a field wired into only one of them was the
// exact failure mode the shared decoder exists to prevent.
func TestSubagentMetadataOnToolUseBlocks(t *testing.T) {
	const line = `{"type":"assistant","parent_tool_use_id":"toolu_parent","subagent_type":"Explore","task_description":"Look around","session_id":"s1","message":{"model":"claude-haiku-4-5-20251001","role":"assistant","content":[{"type":"tool_use","id":"toolu_x","name":"Read","input":{}}]}}`
	var tu *ToolUseEvent
	for _, ev := range parseLines(t, line) {
		if e, ok := ev.(*ToolUseEvent); ok {
			tu = e
		}
	}
	if tu == nil {
		t.Fatal("no ToolUseEvent emitted")
	}
	if tu.Model != "claude-haiku-4-5-20251001" || tu.SubagentType != "Explore" || tu.TaskDescription != "Look around" {
		t.Errorf("ToolUseEvent metadata = {%q, %q, %q}", tu.Model, tu.SubagentType, tu.TaskDescription)
	}
}
