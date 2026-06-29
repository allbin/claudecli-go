package claudecli

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// parseJSONL runs ParseEvents over the given JSONL lines and returns all
// emitted events.
func parseJSONL(t *testing.T, lines ...string) []Event {
	t.Helper()
	r := strings.NewReader(strings.Join(lines, "\n") + "\n")
	ch := make(chan Event, 256)
	go func() {
		defer close(ch)
		ParseEvents(context.Background(), r, ch)
	}()
	var events []Event
	for e := range ch {
		events = append(events, e)
	}
	return events
}

func firstTask(t *testing.T, events []Event) *TaskEvent {
	t.Helper()
	for _, e := range events {
		if te, ok := e.(*TaskEvent); ok {
			return te
		}
	}
	t.Fatal("no TaskEvent emitted")
	return nil
}

func TestParseWorkflowLaunch(t *testing.T) {
	line := `{"type":"user","session_id":"s1","uuid":"u1",` +
		`"tool_use_result":{"status":"async_launched","taskId":"whq0xk3f4","taskType":"local_workflow",` +
		`"workflowName":"echo-marker","runId":"wf_640716cb-556","summary":"sum",` +
		`"transcriptDir":"/p/sess/subagents/workflows/wf_640716cb-556",` +
		`"scriptPath":"/p/sess/workflows/scripts/echo-marker-wf_640716cb-556.js"},` +
		`"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"launched"}]}}`

	var ue *UserEvent
	for _, e := range parseJSONL(t, line) {
		if u, ok := e.(*UserEvent); ok {
			ue = u
		}
	}
	if ue == nil {
		t.Fatal("no UserEvent emitted")
	}
	if ue.WorkflowLaunch == nil {
		t.Fatal("WorkflowLaunch not parsed")
	}
	if ue.AgentResult != nil {
		t.Error("AgentResult should be nil for a workflow launch")
	}
	wl := ue.WorkflowLaunch
	if wl.RunID != "wf_640716cb-556" || wl.WorkflowName != "echo-marker" || wl.TaskType != "local_workflow" {
		t.Errorf("unexpected launch fields: %+v", wl)
	}
	if got, want := wl.ManifestPath(), "/p/sess/workflows/wf_640716cb-556.json"; got != want {
		t.Errorf("ManifestPath = %q, want %q", got, want)
	}
	if got, want := wl.JournalPath(), "/p/sess/subagents/workflows/wf_640716cb-556/journal.jsonl"; got != want {
		t.Errorf("JournalPath = %q, want %q", got, want)
	}
}

func TestOrdinaryAgentResultNotMistakenForWorkflow(t *testing.T) {
	// A normal subagent result must still parse as AgentResult, not a launch.
	line := `{"type":"user","session_id":"s1",` +
		`"tool_use_result":{"status":"completed","agentId":"a1","agentType":"general-purpose",` +
		`"totalTokens":10,"content":[{"type":"text","text":"hi"}]},` +
		`"message":{"role":"user","content":[]}}`
	for _, e := range parseJSONL(t, line) {
		if u, ok := e.(*UserEvent); ok {
			if u.WorkflowLaunch != nil {
				t.Error("ordinary agent result wrongly parsed as WorkflowLaunch")
			}
			if u.AgentResult == nil || u.AgentResult.AgentID != "a1" {
				t.Errorf("AgentResult not parsed: %+v", u.AgentResult)
			}
		}
	}
}

func TestParseTaskEventWorkflowFields(t *testing.T) {
	started := `{"type":"system","subtype":"task_started","task_id":"w8","tool_use_id":"toolu_1",` +
		`"task_type":"local_workflow","workflow_name":"deep-research","description":"d","prompt":"export const meta = {}"}`
	te := firstTask(t, parseJSONL(t, started))
	if !te.IsWorkflow() {
		t.Error("IsWorkflow() = false, want true")
	}
	if te.WorkflowName != "deep-research" {
		t.Errorf("WorkflowName = %q", te.WorkflowName)
	}
	if !strings.Contains(te.Prompt, "meta") {
		t.Errorf("Prompt not carried: %q", te.Prompt)
	}

	progress := `{"type":"system","subtype":"task_progress","task_id":"w8","task_type":"local_workflow",` +
		`"usage":{"total_tokens":100,"tool_uses":2,"duration_ms":50},` +
		`"workflow_progress":[{"type":"workflow_phase","index":1,"title":"Echo"},` +
		`{"type":"workflow_agent","index":1,"label":"L","phaseIndex":1,"phaseTitle":"Echo",` +
		`"agentId":"a1","model":"claude-opus-4-8[1m]","state":"progress","tokens":42,"toolCalls":3}]}`
	tp := firstTask(t, parseJSONL(t, progress))
	if len(tp.WorkflowProgress) != 2 {
		t.Fatalf("WorkflowProgress len = %d, want 2", len(tp.WorkflowProgress))
	}
	if !tp.WorkflowProgress[0].IsPhase() || tp.WorkflowProgress[0].Title != "Echo" {
		t.Errorf("phase entry wrong: %+v", tp.WorkflowProgress[0])
	}
	ag := tp.WorkflowProgress[1]
	if !ag.IsAgent() || ag.AgentID != "a1" || ag.State != "progress" || ag.Tokens != 42 || ag.ToolCalls != 3 || ag.PhaseTitle != "Echo" {
		t.Errorf("agent entry wrong: %+v", ag)
	}
	if tp.TotalTokens != 100 || tp.ToolUses != 2 || tp.DurationMs != 50 {
		t.Errorf("usage not carried: %+v", tp)
	}

	notif := `{"type":"system","subtype":"task_notification","task_id":"w8","status":"completed",` +
		`"output_file":"/p/tasks/w8.output","summary":"done"}`
	tn := firstTask(t, parseJSONL(t, notif))
	if tn.Status != "completed" || tn.OutputFile != "/p/tasks/w8.output" {
		t.Errorf("notification fields wrong: status=%q output=%q", tn.Status, tn.OutputFile)
	}
}

func TestParseTaskUpdated(t *testing.T) {
	line := `{"type":"system","subtype":"task_updated","task_id":"w8","patch":{"status":"completed","end_time":1782725441950}}`
	te := firstTask(t, parseJSONL(t, line))
	if te.Subtype != "task_updated" || te.Status != "completed" || te.EndTime != 1782725441950 {
		t.Errorf("task_updated parse wrong: %+v", te)
	}
}

func TestParseThinkingTokens(t *testing.T) {
	line := `{"type":"system","subtype":"thinking_tokens","estimated_tokens":50,"estimated_tokens_delta":50,"session_id":"s1","uuid":"u2"}`
	var tt *ThinkingTokensEvent
	for _, e := range parseJSONL(t, line) {
		if t2, ok := e.(*ThinkingTokensEvent); ok {
			tt = t2
		}
		if u, ok := e.(*UnknownEvent); ok {
			t.Errorf("thinking_tokens produced UnknownEvent: %s", u.Type)
		}
	}
	if tt == nil {
		t.Fatal("no ThinkingTokensEvent emitted")
	}
	if tt.EstimatedTokens != 50 || tt.EstimatedTokensDelta != 50 || tt.SessionID != "s1" {
		t.Errorf("thinking_tokens fields wrong: %+v", tt)
	}
}

func TestWorkflowLaunchPathsFallback(t *testing.T) {
	// Derive paths from TranscriptDir when ScriptPath is absent.
	wl := &WorkflowLaunch{
		RunID:         "wf_x",
		TranscriptDir: "/home/u/.claude/projects/proj/sess/subagents/workflows/wf_x",
	}
	if got, want := wl.ManifestPath(), "/home/u/.claude/projects/proj/sess/workflows/wf_x.json"; got != want {
		t.Errorf("ManifestPath fallback = %q, want %q", got, want)
	}
	if got, want := wl.JournalPath(), "/home/u/.claude/projects/proj/sess/subagents/workflows/wf_x/journal.jsonl"; got != want {
		t.Errorf("JournalPath = %q, want %q", got, want)
	}
	// Empty launch yields empty paths and the sentinel error on read.
	empty := &WorkflowLaunch{}
	if empty.ManifestPath() != "" {
		t.Error("empty launch should have no manifest path")
	}
	if _, err := ReadWorkflowSnapshot(empty); err != ErrNoManifestPath {
		t.Errorf("ReadWorkflowSnapshot(empty) err = %v, want ErrNoManifestPath", err)
	}
}

// newTempLaunch builds a WorkflowLaunch rooted in a temp dir and returns the
// launch plus a function that writes a manifest with the given status/result.
func newTempLaunch(t *testing.T) (*WorkflowLaunch, func(status, result string)) {
	t.Helper()
	dir := t.TempDir()
	runID := "wf_test-1"
	sessionDir := filepath.Join(dir, "sess")
	scriptsDir := filepath.Join(sessionDir, "workflows", "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	launch := &WorkflowLaunch{
		RunID:         runID,
		ScriptPath:    filepath.Join(scriptsDir, "echo-"+runID+".js"),
		TranscriptDir: filepath.Join(sessionDir, "subagents", "workflows", runID),
	}
	write := func(status, result string) {
		m := map[string]any{"runId": runID, "workflowName": "echo", "status": status}
		if result != "" {
			m["result"] = result
		}
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(launch.ManifestPath(), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return launch, write
}

func TestReadWorkflowSnapshot(t *testing.T) {
	launch, write := newTempLaunch(t)

	// Missing manifest surfaces as a wrapped fs.ErrNotExist.
	if _, err := ReadWorkflowSnapshot(launch); err == nil {
		t.Error("expected error reading nonexistent manifest")
	} else if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected fs.ErrNotExist-wrapped error, got %v", err)
	}

	write("completed", "DONE-MARKER-42")
	snap, err := ReadWorkflowSnapshot(launch)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != "completed" || !snap.IsTerminal() {
		t.Errorf("status = %q, terminal = %v", snap.Status, snap.IsTerminal())
	}
	var result string
	if err := json.Unmarshal(snap.Result, &result); err != nil || result != "DONE-MARKER-42" {
		t.Errorf("result = %q, err = %v", result, err)
	}
	if len(snap.Raw) == 0 {
		t.Error("Raw not preserved")
	}
}

func TestWatchWorkflow(t *testing.T) {
	launch, write := newTempLaunch(t)
	write("running", "")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ch, err := WatchWorkflow(ctx, launch, WithPollInterval(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}

	// First snapshot reflects the running state.
	select {
	case s, ok := <-ch:
		if !ok {
			t.Fatal("channel closed before any snapshot")
		}
		if s.Status != "running" {
			t.Errorf("first snapshot status = %q, want running", s.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no initial snapshot")
	}

	// Advance to terminal; expect a completed snapshot then channel closure.
	write("completed", "DONE")
	var last WorkflowSnapshot
	gotTerminal := false
	deadline := time.After(2 * time.Second)
loop:
	for {
		select {
		case s, ok := <-ch:
			if !ok {
				break loop
			}
			last = s
			if s.IsTerminal() {
				gotTerminal = true
			}
		case <-deadline:
			t.Fatal("channel did not close after terminal status")
		}
	}
	if !gotTerminal || last.Status != "completed" {
		t.Errorf("expected terminal completed snapshot, got terminal=%v last=%q", gotTerminal, last.Status)
	}
}

func TestWatchWorkflowNoManifestPath(t *testing.T) {
	if _, err := WatchWorkflow(context.Background(), &WorkflowLaunch{}, nil); err != ErrNoManifestPath {
		t.Errorf("err = %v, want ErrNoManifestPath", err)
	}
}
