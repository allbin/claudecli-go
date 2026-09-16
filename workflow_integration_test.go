//go:build integration

package claudecli

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"
)

// workflowSleepPrompt asks for a two-phase workflow whose first-phase agents
// block for 75 seconds in a foreground command. The CLI refuses a foreground
// `sleep`, so the delay goes through python.
const workflowSleepPrompt = `ultracode: Run a dynamic workflow with exactly two phases. ` +
	`Phase "Sleep": two agents in parallel labelled "sleeper-a" and "sleeper-b"; each must first Read the file go.mod, ` +
	"then run the foreground Bash command `python3 -c 'import time; time.sleep(75)'` (timeout 300000 ms, not in the background), " +
	`then reply with just DONE-A (or DONE-B). Phase "Join": one agent labelled "joiner" that replies with the concatenation ` +
	`of the two replies it is given. The workflow returns the joiner reply. Do nothing else and do not explain.`

// TestIntegrationWorkflowLiveState runs a real workflow and checks, while its
// agents are still sleeping, that the journal and an agent transcript are
// readable and the run manifest does not exist yet. After the run it checks
// the manifest, the tick kinds, and subagent-owned Bash tasks.
func TestIntegrationWorkflowLiveState(t *testing.T) {
	if testing.Short() {
		t.Skip("launches a live workflow for several minutes")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	client := New(
		WithWorkDir(wd),
		WithPermissionMode(PermissionBypass),
		WithForwardSubagentText(),
	)
	session, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer session.Close()
	if err := session.Query(workflowSleepPrompt); err != nil {
		t.Fatalf("Query: %v", err)
	}

	var (
		launch         *WorkflowLaunch
		workflowDone   bool
		finalResult    bool
		trees, usage   int
		unresolved     []string
		workflowDesc   string
		tickSeq        strings.Builder
		streamToolUses = map[string]bool{}
		parentLinked   int
		ownedBash      = map[string][]*TaskEvent{}

		journalOffset int64
		started       = map[string]WorkflowJournalEntry{}
		results       = map[string]bool{}
		midRunOK      bool
		midRunNote    string
	)

	// pollDisk reads the live files and sets midRunOK once a poll finds the
	// journal listing agents, an agent's Bash tool_use on disk with no result
	// yet, and no manifest.
	pollDisk := func() {
		// Stat the manifest before reading the journal: the CLI writes the
		// journal's last record just before the manifest, so a manifest seen
		// first implies a complete journal.
		_, manifestErr := os.Stat(launch.ManifestPath())
		manifestExists := manifestErr == nil
		entries, next, err := ReadWorkflowJournal(launch, journalOffset)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("ReadWorkflowJournal: %v", err)
			}
			return
		}
		journalOffset = next
		for _, e := range entries {
			switch e.Type {
			case "started":
				started[e.AgentID] = e
			case "result":
				results[e.AgentID] = true
			}
		}
		if manifestExists && len(results) < len(started) {
			t.Errorf("manifest exists while %d agent(s) are still running", len(started)-len(results))
		}
		if workflowDone || midRunOK || manifestExists {
			return
		}
		for id, e := range started {
			if results[id] {
				continue
			}
			evs, _, err := ReadWorkflowAgentTranscript(launch, id, 0)
			if err != nil {
				continue // transcript may not exist for an instant
			}
			pending := map[string]bool{}
			for _, te := range evs {
				switch ev := te.Event.(type) {
				case *ToolUseEvent:
					if ev.Name == "Bash" {
						pending[ev.ID] = true
					}
				case *ToolResultEvent:
					delete(pending, ev.ToolUseID)
				}
			}
			if len(pending) > 0 {
				midRunOK = true
				midRunNote = e.Label + " (" + id + ")"
				if _, err := ReadWorkflowAgentMeta(launch, id); err != nil {
					t.Errorf("ReadWorkflowAgentMeta mid-run: %v", err)
				}
				return
			}
		}
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for !finalResult {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out (launch=%v workflowDone=%v)", launch != nil, workflowDone)
		case <-ticker.C:
			if launch != nil {
				pollDisk()
			}
		case ev, ok := <-session.Events():
			if !ok {
				t.Fatal("event channel closed")
			}
			switch e := ev.(type) {
			case *UserEvent:
				if e.WorkflowLaunch != nil {
					launch = e.WorkflowLaunch
					t.Logf("launched %s run %s", launch.WorkflowName, launch.RunID)
				}
				if e.ParentToolUseID != "" {
					parentLinked++
				}
			case *ToolUseEvent:
				streamToolUses[e.ID] = true
				if e.ParentToolUseID != "" {
					parentLinked++
				}
			case *TextEvent:
				if e.ParentToolUseID != "" {
					parentLinked++
				}
			case *TaskEvent:
				if e.Subtype != "task_progress" {
					t.Logf("%s type=%s task=%s status=%s owned=%v", e.Subtype, e.TaskType, e.TaskID, e.Status, e.OwnedBySubagent)
				}
				if e.OwnedBySubagent {
					ownedBash[e.TaskID] = append(ownedBash[e.TaskID], e)
				}
				if !e.IsWorkflow() {
					continue
				}
				if e.Subtype == "task_started" {
					workflowDesc = e.Description
				}
				if e.Subtype == "task_progress" {
					kind := "n"
					if e.HasWorkflowTree() {
						kind = "T"
						trees++
					} else {
						usage++
					}
					switch {
					case e.WorkflowAgentLabel != "":
						tickSeq.WriteString(kind)
					case e.Description == workflowDesc:
						// A tick about the workflow itself, not an agent.
						tickSeq.WriteString(kind + "*")
						t.Logf("workflow-level tick (tree=%v, last_tool_name=%q): %q", e.HasWorkflowTree(), e.LastToolName, e.Description)
					default:
						tickSeq.WriteString(kind + "?")
						unresolved = append(unresolved, e.Description)
					}
				}
				if e.Subtype == "task_notification" {
					if e.HasWorkflowTree() {
						t.Error("workflow task_notification carries a tree")
					}
					workflowDone = true
					if launch != nil {
						pollDisk()
					}
				}
			case *ErrorEvent:
				if e.Fatal {
					t.Fatalf("fatal: %v", e.Err)
				}
			case *ResultEvent:
				t.Logf("result (workflowDone=%v): %.80q", workflowDone, e.Text)
				if workflowDone {
					finalResult = true
					t.Logf("final result: %q", e.Text)
				}
			}
		}
	}

	if launch == nil {
		t.Fatal("no WorkflowLaunch")
	}
	if !midRunOK {
		t.Error("never observed journal + in-flight agent transcript while the manifest was absent")
	} else {
		t.Logf("mid-run state readable via %s", midRunNote)
	}

	// The manifest lands at the end of the run.
	var snap *WorkflowSnapshot
	for i := 0; i < 20; i++ {
		if snap, err = ReadWorkflowSnapshot(launch); err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("ReadWorkflowSnapshot after completion: %v", err)
	}
	if snap.Status != "completed" {
		t.Errorf("manifest status = %q", snap.Status)
	}

	pollDisk()
	if len(started) != 3 || len(results) != 3 {
		t.Errorf("journal: %d started, %d results, want 3 each", len(started), len(results))
	}
	for id, e := range started {
		evs, _, err := ReadWorkflowAgentTranscript(launch, id, 0)
		if err != nil {
			t.Errorf("%s transcript: %v", e.Label, err)
			continue
		}
		if len(evs) == 0 {
			t.Errorf("%s transcript empty", e.Label)
			continue
		}
		for _, te := range evs {
			if tu, ok := te.Event.(*ToolUseEvent); ok && streamToolUses[tu.ID] {
				t.Errorf("%s tool_use %s was forwarded on the stream", e.Label, tu.ID)
			}
		}
		if _, ok := evs[len(evs)-1].Event.(*TextEvent); !ok {
			t.Errorf("%s transcript ends with %T, want *TextEvent", e.Label, evs[len(evs)-1].Event)
		}
	}

	t.Logf("ticks: %d tree, %d usage, sequence %s (* = workflow-level, ? = unresolved)", trees, usage, tickSeq.String())
	if trees == 0 || usage == 0 {
		t.Errorf("want both tick kinds, got %d tree and %d usage", trees, usage)
	}
	if len(unresolved) > 0 {
		t.Errorf("unresolved tick descriptions: %q", unresolved)
	}
	if parentLinked > 0 {
		t.Logf("note: %d events carried ParentToolUseID (not seen on CLI 2.1.270)", parentLinked)
	}

	if len(ownedBash) != 2 {
		t.Errorf("subagent-owned tasks = %d, want 2 (one Bash per sleeper)", len(ownedBash))
	}
	for id, evs := range ownedBash {
		subtypes := make([]string, 0, len(evs))
		for _, e := range evs {
			subtypes = append(subtypes, e.Subtype)
			if e.TaskType != "local_bash" || e.IsBackgrounded {
				t.Errorf("%s %s: TaskType=%q IsBackgrounded=%v", id, e.Subtype, e.TaskType, e.IsBackgrounded)
			}
		}
		if got := strings.Join(subtypes, ","); !strings.Contains(got, "task_started") || !strings.Contains(got, "task_notification") {
			t.Errorf("%s owned subtypes = %s", id, got)
		}
	}
}
