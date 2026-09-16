package claudecli

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fixtures in testdata/workflow were captured from CLI 2.1.270 runs of a
// two-phase workflow ("Sleep": sleeper-a and sleeper-b each Read a file and run
// a 90s foreground python sleep; "Join": joiner) and of a phase-less workflow
// whose labels contain ": ". Trimmed: attachment bodies, the workflow script,
// long prompt text, and home paths.
const (
	workflowFixtureDir = "testdata/workflow"
	sleeperAID         = "acf991b72ff3c46ab"
	joinerID           = "adfbf396645c0cea2"
)

// decodeFixtureTasks runs every line of a captured stream through the decoder
// both loops share, with one backfiller for the whole file. ParseEvents would
// stop at the first of the workflow's two results.
func decodeFixtureTasks(t *testing.T, name string) []*TaskEvent {
	t.Helper()
	f, err := os.Open(filepath.Join(workflowFixtureDir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	backfill := newTaskTypeBackfiller()
	var tasks []*TaskEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var raw rawEvent
		if err := json.Unmarshal(sc.Bytes(), &raw); err != nil {
			t.Fatalf("fixture line: %v", err)
		}
		ev, _ := decodeStatelessEvent(&raw, sc.Bytes(), backfill)
		if te, ok := ev.(*TaskEvent); ok {
			tasks = append(tasks, te)
		}
	}
	return tasks
}

func TestWorkflowTickKindsPhasedFixture(t *testing.T) {
	tasks := decodeFixtureTasks(t, "stream-phased.jsonl")

	var kinds strings.Builder
	var trees, usage int
	for _, te := range tasks {
		if !te.IsWorkflow() || te.Subtype != "task_progress" {
			continue
		}
		if te.HasWorkflowTree() {
			kinds.WriteByte('T')
			trees++
		} else {
			kinds.WriteByte('n')
			usage++
		}
		if te.WorkflowAgentLabel == "" {
			t.Errorf("tick %q: WorkflowAgentLabel not resolved", te.Description)
		}
		if got := te.WorkflowPhaseTitle + ": " + te.WorkflowAgentLabel; got != te.Description {
			t.Errorf("resolved %q, description %q", got, te.Description)
		}
		// The CLI puts the agent label in last_tool_name on workflow ticks.
		if te.LastToolName != te.WorkflowAgentLabel {
			t.Errorf("LastToolName = %q, want label %q", te.LastToolName, te.WorkflowAgentLabel)
		}
		if te.WorkflowAgentID == "" {
			t.Errorf("tick %q: WorkflowAgentID not resolved", te.Description)
		}
	}
	if trees == 0 || usage == 0 {
		t.Fatalf("want both tick kinds, got %s", kinds.String())
	}
	t.Logf("tick sequence: %s", kinds.String())

	// Usage ticks carry workflow-wide totals: they never decrease.
	last := 0
	for _, te := range tasks {
		if te.IsWorkflow() && te.Subtype == "task_progress" {
			if te.TotalTokens < last {
				t.Errorf("TotalTokens decreased: %d after %d", te.TotalTokens, last)
			}
			last = te.TotalTokens
		}
	}
}

// Every event of the workflow task, including the task_updated and
// task_notification that close it, classifies as a workflow.
func TestWorkflowLifecycleClassifiedFixture(t *testing.T) {
	for _, name := range []string{"stream-phased.jsonl", "stream-unphased.jsonl"} {
		tasks := decodeFixtureTasks(t, name)
		workflowID := ""
		for _, te := range tasks {
			if te.Subtype == "task_started" && te.IsWorkflow() {
				workflowID = te.TaskID
			}
		}
		var subtypes []string
		for _, te := range tasks {
			if te.TaskID != workflowID {
				continue
			}
			if !te.IsWorkflow() {
				t.Errorf("%s: %s not classified as workflow", name, te.Subtype)
			}
			if te.Subtype != "task_progress" {
				subtypes = append(subtypes, te.Subtype)
			}
		}
		if got := strings.Join(subtypes, ","); got != "task_started,task_updated,task_notification" {
			t.Errorf("%s: lifecycle = %s", name, got)
		}
	}
}

func TestWorkflowTickResolvesAgentIDsFromTree(t *testing.T) {
	tasks := decodeFixtureTasks(t, "stream-phased.jsonl")
	ids := map[string]string{}
	for _, te := range tasks {
		if te.WorkflowAgentLabel != "" {
			if prev, ok := ids[te.WorkflowAgentLabel]; ok && prev != te.WorkflowAgentID {
				t.Errorf("label %s resolved to %s and %s", te.WorkflowAgentLabel, prev, te.WorkflowAgentID)
			}
			ids[te.WorkflowAgentLabel] = te.WorkflowAgentID
		}
	}
	if ids["sleeper-a"] != sleeperAID || ids["joiner"] != joinerID {
		t.Errorf("resolved ids = %v", ids)
	}
}

func TestWorkflowTickUnphasedLabelWithColon(t *testing.T) {
	tasks := decodeFixtureTasks(t, "stream-unphased.jsonl")
	seen := 0
	for _, te := range tasks {
		if !te.IsWorkflow() || te.Subtype != "task_progress" {
			continue
		}
		seen++
		// Labels are "check: one"/"check: two" with no phase. Splitting the
		// description on ": " would wrongly yield phase "check".
		if te.WorkflowPhaseTitle != "" {
			t.Errorf("WorkflowPhaseTitle = %q, want empty", te.WorkflowPhaseTitle)
		}
		if te.WorkflowAgentLabel != te.Description || !strings.HasPrefix(te.WorkflowAgentLabel, "check: ") {
			t.Errorf("WorkflowAgentLabel = %q, description %q", te.WorkflowAgentLabel, te.Description)
		}
	}
	if seen == 0 {
		t.Fatal("no workflow progress ticks")
	}
}

func TestResolveWorkflowAgentAmbiguous(t *testing.T) {
	agents := []workflowAgentRef{
		{phaseTitle: "a", label: "b", agentID: "x1"},
		{label: "a: b", agentID: "x2"},
	}
	ev := &TaskEvent{Description: "a: b"}
	resolveWorkflowAgent(ev, agents)
	if ev.WorkflowAgentLabel != "" || ev.WorkflowPhaseTitle != "" || ev.WorkflowAgentID != "" {
		t.Errorf("ambiguous description resolved: %+v", ev)
	}

	// Same label twice in one phase with different ids: label resolves, id not.
	agents = []workflowAgentRef{{phaseTitle: "p", label: "l", agentID: "x1"}, {phaseTitle: "p", label: "l", agentID: "x2"}}
	ev = &TaskEvent{Description: "p: l"}
	resolveWorkflowAgent(ev, agents)
	if ev.WorkflowAgentLabel != "l" || ev.WorkflowAgentID != "" {
		t.Errorf("duplicate label: %+v", ev)
	}

	ev = &TaskEvent{Description: "nobody"}
	resolveWorkflowAgent(ev, agents)
	if ev.WorkflowAgentLabel != "" {
		t.Errorf("unknown description resolved: %+v", ev)
	}
}

func TestOwnedBySubagentBashFixture(t *testing.T) {
	tasks := decodeFixtureTasks(t, "stream-phased.jsonl")
	byID := map[string][]*TaskEvent{}
	for _, te := range tasks {
		if te.TaskType == "local_bash" {
			byID[te.TaskID] = append(byID[te.TaskID], te)
		}
	}
	if len(byID) != 2 {
		t.Fatalf("want 2 bash tasks, got %d", len(byID))
	}
	bashToolUseIDs := map[string]bool{}
	for id, evs := range byID {
		var sawNotif bool
		for _, te := range evs {
			if !te.OwnedBySubagent {
				t.Errorf("%s %s: OwnedBySubagent = false", id, te.Subtype)
			}
			if te.IsBackgrounded {
				t.Errorf("%s %s: IsBackgrounded = true", id, te.Subtype)
			}
			if te.Subtype == "task_notification" {
				sawNotif = true
				// The CLI omits owned_by_subagent here; it is backfilled.
				if strings.Contains(string(te.Raw), "owned_by_subagent") {
					t.Errorf("fixture notification carries owned_by_subagent; backfill no longer needed?")
				}
			}
			bashToolUseIDs[te.ToolUseID] = true
		}
		if !sawNotif {
			t.Errorf("%s: no task_notification", id)
		}
	}

	// The bash task's ToolUseID is the Bash tool_use in the owning agent's
	// transcript.
	evs, _, err := ReadWorkflowAgentTranscript(fixtureLaunch("run"), sleeperAID, 0)
	if err != nil {
		t.Fatal(err)
	}
	linked := false
	for _, e := range evs {
		if tu, ok := e.Event.(*ToolUseEvent); ok && tu.Name == "Bash" && bashToolUseIDs[tu.ID] {
			linked = true
		}
	}
	if !linked {
		t.Error("no agent Bash tool_use matches a subagent-owned bash task")
	}
}

func fixtureLaunch(sub string) *WorkflowLaunch {
	return &WorkflowLaunch{RunID: "wf_1ed45b93-047", TranscriptDir: filepath.Join(workflowFixtureDir, sub)}
}

func TestReadWorkflowJournalFixture(t *testing.T) {
	entries, next, err := ReadWorkflowJournal(fixtureLaunch("run"), 0)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(workflowFixtureDir, "run", "journal.jsonl"))
	if next != info.Size() {
		t.Errorf("next = %d, want file size %d", next, info.Size())
	}
	var types []string
	for _, e := range entries {
		types = append(types, e.Type)
		if len(e.Raw) == 0 {
			t.Error("Raw not preserved")
		}
	}
	if got := strings.Join(types, ","); got != "launched,started,started,result,result,started,result" {
		t.Errorf("types = %s", got)
	}
	s := entries[1]
	if s.AgentID != sleeperAID || s.Label != "sleeper-a" || s.Phase != "Sleep" || !strings.HasPrefix(s.Key, "v2:") {
		t.Errorf("started entry = %+v", s)
	}
	var result string
	if err := json.Unmarshal(entries[6].Result, &result); err != nil || result != "DONE-ADONE-B" {
		t.Errorf("joiner result = %q (%v)", result, err)
	}

	// Nothing new past the end.
	more, again, err := ReadWorkflowJournal(fixtureLaunch("run"), next)
	if err != nil || len(more) != 0 || again != next {
		t.Errorf("re-read at EOF: %d entries, next %d, err %v", len(more), again, err)
	}
}

// tempRun returns a launch whose transcript dir is an empty temp dir.
func tempRun(t *testing.T) *WorkflowLaunch {
	t.Helper()
	return &WorkflowLaunch{RunID: "wf_t", TranscriptDir: t.TempDir()}
}

func TestReadWorkflowJournalIncrementalPartialLine(t *testing.T) {
	full, err := os.ReadFile(filepath.Join(workflowFixtureDir, "run", "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	launch := tempRun(t)
	path := launch.JournalPath()

	// Replay the file as the CLI would write it, cut at every byte, and check
	// the poller never returns a partial or duplicate entry.
	var got []WorkflowJournalEntry
	var offset int64
	for cut := 0; cut <= len(full); cut++ {
		if err := os.WriteFile(path, full[:cut], 0o644); err != nil {
			t.Fatal(err)
		}
		entries, next, err := ReadWorkflowJournal(launch, offset)
		if err != nil {
			t.Fatalf("cut %d: %v", cut, err)
		}
		got = append(got, entries...)
		offset = next
	}
	if len(got) != 7 {
		t.Fatalf("got %d entries, want 7", len(got))
	}
	for i, e := range got {
		if !json.Valid(e.Raw) {
			t.Errorf("entry %d raw invalid", i)
		}
	}
	if offset != int64(len(full)) {
		t.Errorf("final offset %d, want %d", offset, len(full))
	}
}

func TestReadJSONLFromEdgeCases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.jsonl")

	// Complete trailing object without a newline is consumed; a later newline
	// shows up as a blank line and is skipped.
	os.WriteFile(path, []byte(`{"type":"a"}`+"\n"+`{"type":"b"}`), 0o644)
	lines, next, err := readJSONLFrom(path, 0)
	if err != nil || len(lines) != 2 || next != 25 {
		t.Fatalf("lines=%d next=%d err=%v", len(lines), next, err)
	}
	os.WriteFile(path, []byte(`{"type":"a"}`+"\n"+`{"type":"b"}`+"\n"+`not json`+"\n"+`{"type":"c"}`+"\n"), 0o644)
	lines, next, err = readJSONLFrom(path, next)
	if err != nil || len(lines) != 1 || string(lines[0]) != `{"type":"c"}` || next != 48 {
		t.Fatalf("after append: lines=%q next=%d err=%v", lines, next, err)
	}

	// Offset past EOF: the file was replaced.
	os.WriteFile(path, []byte(`{}`+"\n"), 0o644)
	if _, _, err := readJSONLFrom(path, next); !errors.Is(err, ErrOffsetBeyondEOF) {
		t.Errorf("err = %v, want ErrOffsetBeyondEOF", err)
	}

	// Missing file.
	if _, _, err := ReadWorkflowJournal(&WorkflowLaunch{TranscriptDir: filepath.Join(dir, "nope")}, 0); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing journal err = %v", err)
	}
	if _, _, err := ReadWorkflowJournal(&WorkflowLaunch{}, 0); !errors.Is(err, ErrNoTranscriptDir) {
		t.Errorf("empty launch err = %v", err)
	}
}

func TestWorkflowAgentIDValidation(t *testing.T) {
	launch := fixtureLaunch("run")
	for _, bad := range []string{"", "../x", "a/b", "ABC", "a.b", "a b", "..", "a\x00"} {
		if _, err := launch.AgentTranscriptPath(bad); !errors.Is(err, ErrInvalidAgentID) {
			t.Errorf("AgentTranscriptPath(%q) err = %v", bad, err)
		}
		if _, _, err := ReadWorkflowAgentTranscript(launch, bad, 0); !errors.Is(err, ErrInvalidAgentID) {
			t.Errorf("ReadWorkflowAgentTranscript(%q) err = %v", bad, err)
		}
		if _, err := ReadWorkflowAgentMeta(launch, bad); !errors.Is(err, ErrInvalidAgentID) {
			t.Errorf("ReadWorkflowAgentMeta(%q) err = %v", bad, err)
		}
	}
	p, err := launch.AgentMetaPath(sleeperAID)
	if err != nil || p != filepath.Join(workflowFixtureDir, "run", "agent-"+sleeperAID+".meta.json") {
		t.Errorf("AgentMetaPath = %q, %v", p, err)
	}
	if _, err := (&WorkflowLaunch{}).AgentTranscriptPath("abc"); !errors.Is(err, ErrNoTranscriptDir) {
		t.Errorf("no dir err = %v", err)
	}
	// ScriptPath fallback.
	sp := &WorkflowLaunch{RunID: "wf_x", ScriptPath: "/p/sess/workflows/scripts/n-wf_x.js"}
	if p, _ := sp.AgentTranscriptPath("abc"); p != "/p/sess/subagents/workflows/wf_x/agent-abc.jsonl" {
		t.Errorf("fallback path = %q", p)
	}
}

func TestReadWorkflowAgentMetaFixture(t *testing.T) {
	m, err := ReadWorkflowAgentMeta(fixtureLaunch("run"), joinerID)
	if err != nil {
		t.Fatal(err)
	}
	if m.AgentType != "workflow-subagent" || m.Description != "joiner" || m.WorkflowPhase != "Join" || len(m.Raw) == 0 {
		t.Errorf("meta = %+v", m)
	}
	if _, err := ReadWorkflowAgentMeta(fixtureLaunch("run"), "abc"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing meta err = %v", err)
	}
}

func TestReadWorkflowAgentTranscriptFixture(t *testing.T) {
	evs, next, err := ReadWorkflowAgentTranscript(fixtureLaunch("run"), sleeperAID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range evs {
		if e.UUID == "" || e.Timestamp.IsZero() {
			t.Errorf("event %T missing uuid/timestamp", e.Event)
		}
		switch ev := e.Event.(type) {
		case *UserEvent:
			kinds = append(kinds, "user")
			if !strings.Contains(ev.Text(), "/etc/hostname") {
				t.Errorf("prompt text = %q", ev.Text())
			}
		case *ToolUseEvent:
			kinds = append(kinds, "tool_use:"+ev.Name)
			if ev.Model != "claude-opus-5" || len(ev.Input) == 0 {
				t.Errorf("tool use = %+v", ev)
			}
		case *ToolResultEvent:
			kinds = append(kinds, "tool_result")
			if ev.ToolUseID == "" || ev.Text() == "" {
				t.Errorf("tool result = %+v", ev)
			}
		case *TextEvent:
			kinds = append(kinds, "text")
			if strings.TrimSpace(ev.Content) != "DONE-A" {
				t.Errorf("text = %q", ev.Content)
			}
		default:
			t.Errorf("unexpected event %T", ev)
		}
	}
	want := "user,tool_use:Read,tool_result,tool_use:Bash,tool_result,text"
	if got := strings.Join(kinds, ","); got != want {
		t.Errorf("events = %s, want %s", got, want)
	}
	if info, _ := os.Stat(filepath.Join(workflowFixtureDir, "run", "agent-"+sleeperAID+".jsonl")); next != info.Size() {
		t.Errorf("next = %d, want %d", next, info.Size())
	}
}

func TestReadWorkflowAgentTranscriptMidRunFixture(t *testing.T) {
	// Snapshot taken 50s into the run, while the Bash sleep was executing: the
	// tool_use is on disk, its result is not.
	evs, _, err := ReadWorkflowAgentTranscript(fixtureLaunch("midrun"), sleeperAID, 0)
	if err != nil {
		t.Fatal(err)
	}
	last := evs[len(evs)-1].Event
	if tu, ok := last.(*ToolUseEvent); !ok || tu.Name != "Bash" {
		t.Errorf("last mid-run event = %v, want Bash tool_use", last)
	}
	entries, _, err := ReadWorkflowJournal(fixtureLaunch("midrun"), 0)
	if err != nil || len(entries) != 3 {
		t.Errorf("mid-run journal: %d entries, err %v", len(entries), err)
	}
}

func TestReadWorkflowAgentTranscriptIncremental(t *testing.T) {
	full, err := os.ReadFile(filepath.Join(workflowFixtureDir, "run", "agent-"+sleeperAID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	launch := tempRun(t)
	path, _ := launch.AgentTranscriptPath(sleeperAID)
	var total int
	var offset int64
	// Cut in coarse steps; the byte-exact case is covered on the journal.
	for cut := 0; cut <= len(full); cut += 97 {
		os.WriteFile(path, full[:cut], 0o644)
		evs, next, err := ReadWorkflowAgentTranscript(launch, sleeperAID, offset)
		if err != nil {
			t.Fatal(err)
		}
		total += len(evs)
		offset = next
	}
	os.WriteFile(path, full, 0o644)
	evs, _, _ := ReadWorkflowAgentTranscript(launch, sleeperAID, offset)
	total += len(evs)
	if total != 6 {
		t.Errorf("incremental total = %d events, want 6", total)
	}
}

func TestReadWorkflowAgentTranscriptSkipsUnknown(t *testing.T) {
	launch := tempRun(t)
	path, _ := launch.AgentTranscriptPath("abc123")
	// Synthetic lines: the fixture runs produced no thinking blocks.
	lines := []string{
		`{"type":"summary","summary":"x"}`,
		`{"type":"attachment","attachment":{"type":"date"}}`,
		`{"type":"assistant","uuid":"u1","timestamp":"2026-09-16T14:09:08.678Z","message":{"model":"m","content":[{"type":"thinking","thinking":"hmm","signature":"sig"},{"type":"brand_new_block"}]}}`,
		`{"type":"assistant","message":"just a string"}`,
		`{"type":"user","message":{"content":[{"type":"image"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":42}]}}`,
		`{"type":"progress","data":{}}`,
	}
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	evs, _, err := ReadWorkflowAgentTranscript(launch, "abc123", 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range evs {
		switch ev := e.Event.(type) {
		case *ThinkingEvent:
			got = append(got, "thinking:"+ev.Content+":"+ev.Signature+":"+ev.Model)
		case *TextEvent:
			got = append(got, "text:"+ev.Content)
		default:
			got = append(got, "other")
		}
	}
	// The string-form assistant message decodes as text, as on the stream.
	if strings.Join(got, ",") != "thinking:hmm:sig:m,text:just a string" {
		t.Errorf("events = %v", got)
	}
}

func TestReadWorkflowAgentTranscriptToolResultIsError(t *testing.T) {
	// The fixture's Read result omits is_error and its Bash result sends
	// false; neither may read as flagged.
	evs, _, err := ReadWorkflowAgentTranscript(fixtureLaunch("run"), sleeperAID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var results int
	for _, e := range evs {
		if tr, ok := e.Event.(*ToolResultEvent); ok {
			results++
			if tr.IsError {
				t.Errorf("fixture result %s IsError = true, want false", tr.ToolUseID)
			}
		}
	}
	if results != 2 {
		t.Fatalf("fixture tool results = %d, want 2", results)
	}

	// Hand-built: the fixture run had no failing tool call.
	launch := tempRun(t)
	path, _ := launch.AgentTranscriptPath("abc123")
	lines := []string{
		`{"type":"user","uuid":"u1","timestamp":"2026-09-16T14:09:08.678Z","message":{"role":"user","content":[{"tool_use_id":"toolu_bad","type":"tool_result","content":"<tool_use_error>File does not exist.</tool_use_error>","is_error":true}]}}`,
		`{"type":"user","uuid":"u2","timestamp":"2026-09-16T14:09:09.678Z","message":{"role":"user","content":[{"tool_use_id":"toolu_ok","type":"tool_result","content":"fine","is_error":false}]}}`,
	}
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	evs, _, err = ReadWorkflowAgentTranscript(launch, "abc123", 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range evs {
		if tr, ok := e.Event.(*ToolResultEvent); ok {
			got[tr.ToolUseID] = tr.IsError
		}
	}
	if len(got) != 2 || !got["toolu_bad"] || got["toolu_ok"] {
		t.Errorf("IsError by tool use = %v, want toolu_bad:true toolu_ok:false", got)
	}
}

func TestJournalPathScriptPathFallback(t *testing.T) {
	// Before 0.10.0 this fallback resolved to <session>/workflows/subagents/...
	l := &WorkflowLaunch{RunID: "wf_x", ScriptPath: "/p/sess/workflows/scripts/n-wf_x.js"}
	if got, want := l.JournalPath(), "/p/sess/subagents/workflows/wf_x/journal.jsonl"; got != want {
		t.Errorf("JournalPath = %q, want %q", got, want)
	}
}

func TestReadWorkflowSnapshotFixture(t *testing.T) {
	// The manifest the CLI wrote when the fixture run ended.
	data, err := os.ReadFile(filepath.Join(workflowFixtureDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	launch := &WorkflowLaunch{RunID: "wf_1ed45b93-047", ScriptPath: filepath.Join(dir, "workflows", "scripts", "sleep-join-wf_1ed45b93-047.js")}
	if err := os.MkdirAll(filepath.Dir(launch.ScriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launch.ManifestPath(), data, 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := ReadWorkflowSnapshot(launch)
	if err != nil {
		t.Fatal(err)
	}
	var result string
	_ = json.Unmarshal(snap.Result, &result)
	if !snap.IsTerminal() || result != "DONE-ADONE-B" || snap.AgentCount != 3 || len(snap.Agents()) != 3 || len(snap.Phases) != 2 {
		t.Errorf("snapshot = status %q result %q agents %d/%d phases %d", snap.Status, result, snap.AgentCount, len(snap.Agents()), len(snap.Phases))
	}
}
