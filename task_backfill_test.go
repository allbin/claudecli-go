package claudecli

import (
	"bytes"
	"context"
	"testing"
)

// collectTasks returns the TaskEvents from a slice of events, in order.
func collectTasks(events []Event) []*TaskEvent {
	var tasks []*TaskEvent
	for _, e := range events {
		if te, ok := e.(*TaskEvent); ok {
			tasks = append(tasks, te)
		}
	}
	return tasks
}

// The CLI stamps task_type only on task_started; later task_progress /
// task_updated / task_notification events for the same task_id omit it. The
// backfiller must restore task_type and workflow_name so IsWorkflow() and
// WorkflowName stay correct across the whole lifecycle (claude CLI 2.1.197).
func TestTaskTypeBackfillParseEvents(t *testing.T) {
	started := `{"type":"system","subtype":"task_started","task_id":"w8","tool_use_id":"toolu_1",` +
		`"task_type":"local_workflow","workflow_name":"deep-research","description":"d"}`
	progress := `{"type":"system","subtype":"task_progress","task_id":"w8","usage":{"total_tokens":10}}`
	updated := `{"type":"system","subtype":"task_updated","task_id":"w8","patch":{"status":"running"}}`
	notif := `{"type":"system","subtype":"task_notification","task_id":"w8","status":"completed","summary":"done"}`

	tasks := collectTasks(parseJSONL(t, started, progress, updated, notif))
	if len(tasks) != 4 {
		t.Fatalf("got %d TaskEvents, want 4", len(tasks))
	}

	// Every event across the lifecycle must classify as a workflow and carry
	// the workflow name — not just task_started.
	for _, te := range tasks {
		if !te.IsWorkflow() {
			t.Errorf("subtype %q: IsWorkflow() = false, want true", te.Subtype)
		}
		if te.WorkflowName != "deep-research" {
			t.Errorf("subtype %q: WorkflowName = %q, want deep-research", te.Subtype, te.WorkflowName)
		}
	}
}

// Backfill must touch only the typed convenience fields, never the raw line.
func TestTaskTypeBackfillPreservesRaw(t *testing.T) {
	started := `{"type":"system","subtype":"task_started","task_id":"w8","task_type":"local_workflow","workflow_name":"echo"}`
	progress := `{"type":"system","subtype":"task_progress","task_id":"w8","usage":{"total_tokens":10}}`

	tasks := collectTasks(parseJSONL(t, started, progress))
	if len(tasks) != 2 {
		t.Fatalf("got %d TaskEvents, want 2", len(tasks))
	}
	prog := tasks[1]
	if !prog.IsWorkflow() || prog.WorkflowName != "echo" {
		t.Fatalf("progress not backfilled: type=%q name=%q", prog.TaskType, prog.WorkflowName)
	}
	// The raw line never carried task_type/workflow_name, so backfilling the
	// typed fields must not have rewritten Raw.
	if bytes.Contains(prog.Raw, []byte("task_type")) || bytes.Contains(prog.Raw, []byte("workflow_name")) {
		t.Errorf("Raw was rewritten: %s", prog.Raw)
	}
}

// Ordinary local_agent subagents get their own task_type backfilled but must
// never be misclassified as workflows.
func TestTaskTypeBackfillOrdinaryAgent(t *testing.T) {
	started := `{"type":"system","subtype":"task_started","task_id":"a1","task_type":"local_agent","description":"d"}`
	progress := `{"type":"system","subtype":"task_progress","task_id":"a1","usage":{"total_tokens":10}}`
	notif := `{"type":"system","subtype":"task_notification","task_id":"a1","status":"completed"}`

	for _, te := range collectTasks(parseJSONL(t, started, progress, notif)) {
		if te.IsWorkflow() {
			t.Errorf("subtype %q: local_agent misclassified as workflow", te.Subtype)
		}
		if te.TaskType != "local_agent" {
			t.Errorf("subtype %q: TaskType = %q, want local_agent", te.Subtype, te.TaskType)
		}
		if te.WorkflowName != "" {
			t.Errorf("subtype %q: WorkflowName = %q, want empty", te.Subtype, te.WorkflowName)
		}
	}
}

// Interleaved tasks must not cross-contaminate: each task_id backfills only
// its own identity.
func TestTaskTypeBackfillInterleaved(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"task_started","task_id":"w8","task_type":"local_workflow","workflow_name":"wf"}`,
		`{"type":"system","subtype":"task_started","task_id":"a1","task_type":"local_agent"}`,
		`{"type":"system","subtype":"task_progress","task_id":"a1","usage":{"total_tokens":1}}`,
		`{"type":"system","subtype":"task_progress","task_id":"w8","usage":{"total_tokens":2}}`,
	}
	tasks := collectTasks(parseJSONL(t, lines...))
	byID := map[string]*TaskEvent{}
	for _, te := range tasks {
		if te.Subtype == "task_progress" {
			byID[te.TaskID] = te
		}
	}
	if p := byID["w8"]; p == nil || !p.IsWorkflow() || p.WorkflowName != "wf" {
		t.Errorf("w8 progress = %+v, want workflow wf", p)
	}
	if p := byID["a1"]; p == nil || p.IsWorkflow() || p.TaskType != "local_agent" {
		t.Errorf("a1 progress = %+v, want local_agent non-workflow", p)
	}
}

// The map entry is pruned once a task reaches a terminal status, so a task_id
// reused by a fresh (unclassified) task does not inherit stale identity.
func TestTaskTypeBackfillPrunesOnTerminal(t *testing.T) {
	b := newTaskTypeBackfiller()
	b.apply(&TaskEvent{Subtype: "task_started", TaskID: "w8", TaskType: "local_workflow", WorkflowName: "wf"})
	b.apply(&TaskEvent{Subtype: "task_notification", TaskID: "w8", Status: "completed"})
	if _, ok := b.byID["w8"]; ok {
		t.Fatal("entry not pruned after terminal status")
	}
	// A later event with no task_type must not be backfilled from the pruned
	// entry.
	stray := b.apply(&TaskEvent{Subtype: "task_progress", TaskID: "w8"})
	if stray.IsWorkflow() || stray.WorkflowName != "" {
		t.Errorf("stray event inherited pruned identity: %+v", stray)
	}
}

// The same backfill must apply on the interactive Session (readLoop) decode
// path, not just ParseEvents.
func TestTaskTypeBackfillSession(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)
		sim.send(`{"type":"system","subtype":"task_started","task_id":"w8","task_type":"local_workflow","workflow_name":"deep-research"}`)
		sim.send(`{"type":"system","subtype":"task_progress","task_id":"w8","usage":{"total_tokens":10}}`)
		sim.send(`{"type":"system","subtype":"task_updated","task_id":"w8","patch":{"status":"running"}}`)
		sim.send(`{"type":"system","subtype":"task_notification","task_id":"w8","status":"completed","summary":"done"}`)
		sim.bidi.StdoutWriter.Close()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	var events []Event
	for e := range session.Events() {
		events = append(events, e)
	}

	tasks := collectTasks(events)
	if len(tasks) != 4 {
		t.Fatalf("got %d TaskEvents, want 4 (task_updated must not fall through to UnknownEvent)", len(tasks))
	}
	for _, te := range tasks {
		if !te.IsWorkflow() {
			t.Errorf("subtype %q: IsWorkflow() = false, want true", te.Subtype)
		}
		if te.WorkflowName != "deep-research" {
			t.Errorf("subtype %q: WorkflowName = %q, want deep-research", te.Subtype, te.WorkflowName)
		}
	}
}
