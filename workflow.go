package claudecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// WorkflowLaunch reports that a dynamic workflow
// (https://code.claude.com/docs/en/workflows) was launched in the
// background. It is parsed from the "async_launched" tool_use_result the
// CLI emits when a workflow starts, and is exposed on UserEvent.WorkflowLaunch.
//
// The workflow itself streams progress through TaskEvent
// (TaskType == "local_workflow"); the final answer arrives as a later
// TextEvent / ResultEvent in the same session. A WorkflowLaunch is the
// handle for reading the run's on-disk state. While the run is live, read
// the journal (ReadWorkflowJournal) and each agent's transcript and meta
// (ReadWorkflowAgentTranscript, ReadWorkflowAgentMeta). Once it has ended,
// ReadWorkflowSnapshot reads the manifest with the final result.
type WorkflowLaunch struct {
	Status        string `json:"status"` // "async_launched"
	TaskID        string `json:"taskId"`
	TaskType      string `json:"taskType"` // "local_workflow"
	WorkflowName  string `json:"workflowName"`
	RunID         string `json:"runId"`
	Summary       string `json:"summary"`
	TranscriptDir string `json:"transcriptDir"`
	ScriptPath    string `json:"scriptPath"`
}

// parseWorkflowLaunch returns a WorkflowLaunch when data is a workflow-launch
// tool_use_result, or nil otherwise. The "async_launched" status alone is not
// sufficient — background subagents share it — so a launch is recognised only
// by the workflow markers (task type or a runId paired with a script path).
func parseWorkflowLaunch(data json.RawMessage) *WorkflowLaunch {
	var wl WorkflowLaunch
	if err := json.Unmarshal(data, &wl); err != nil {
		return nil
	}
	if wl.TaskType == "local_workflow" || (wl.RunID != "" && wl.ScriptPath != "") {
		return &wl
	}
	return nil
}

// ManifestPath returns the path to the workflow's run-state manifest
// (<session>/workflows/<runId>.json), derived from ScriptPath. Returns ""
// if the path cannot be derived.
//
// The CLI writes the manifest once, when the run reaches a terminal status
// (CLI 2.1.270: in the same instant as the journal's last record, before
// the workflow's task_notification reaches the stream). It does not exist
// while the run is live; use JournalPath and the agent transcripts for
// that. It survives --no-session-persistence. Its layout is an
// undocumented CLI implementation detail and may change between versions.
func (l *WorkflowLaunch) ManifestPath() string {
	if l == nil || l.RunID == "" {
		return ""
	}
	// ScriptPath is <session>/workflows/scripts/<name>-<runId>.js, so the
	// manifest sits two directories up alongside the scripts/ dir.
	if l.ScriptPath != "" {
		return filepath.Join(filepath.Dir(filepath.Dir(l.ScriptPath)), l.RunID+".json")
	}
	// Fall back via TranscriptDir: <session>/subagents/workflows/<runId>.
	if l.TranscriptDir != "" {
		session := filepath.Dir(filepath.Dir(filepath.Dir(l.TranscriptDir)))
		return filepath.Join(session, "workflows", l.RunID+".json")
	}
	return ""
}

// JournalPath returns the path to the workflow's append-only journal
// (<transcriptDir>/journal.jsonl). Returns "" if it cannot be derived. The
// CLI appends a record as the run launches and as each agent starts and
// finishes; read it with ReadWorkflowJournal.
func (l *WorkflowLaunch) JournalPath() string {
	dir := l.transcriptDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "journal.jsonl")
}

// WorkflowProgressEntry is one entry in a workflow's progress list. The same
// shape appears in the stream (TaskEvent.WorkflowProgress) and in the on-disk
// manifest (WorkflowSnapshot.Progress). Type distinguishes the two kinds:
// "workflow_phase" entries carry only Index and Title; "workflow_agent"
// entries describe a single subagent. Unknown fields are ignored; consult the
// owning event's or snapshot's raw JSON for forward compatibility.
type WorkflowProgressEntry struct {
	Type  string `json:"type"` // "workflow_agent" or "workflow_phase"
	Index int    `json:"index"`

	// workflow_phase
	Title string `json:"title,omitempty"`

	// workflow_agent
	Label           string `json:"label,omitempty"`
	PhaseIndex      int    `json:"phaseIndex,omitempty"`
	PhaseTitle      string `json:"phaseTitle,omitempty"`
	AgentID         string `json:"agentId,omitempty"`
	Model           string `json:"model,omitempty"`
	State           string `json:"state,omitempty"` // "queued","start","progress","done","error"
	Attempt         int    `json:"attempt,omitempty"`
	LastToolName    string `json:"lastToolName,omitempty"`
	LastToolSummary string `json:"lastToolSummary,omitempty"`
	PromptPreview   string `json:"promptPreview,omitempty"`
	ResultPreview   string `json:"resultPreview,omitempty"`
	Tokens          int    `json:"tokens,omitempty"`
	ToolCalls       int    `json:"toolCalls,omitempty"`
	DurationMs      int    `json:"durationMs,omitempty"`
	QueuedAt        int64  `json:"queuedAt,omitempty"`
	StartedAt       int64  `json:"startedAt,omitempty"`
	LastProgressAt  int64  `json:"lastProgressAt,omitempty"`
}

// IsAgent reports whether this entry describes a subagent.
func (e WorkflowProgressEntry) IsAgent() bool { return e.Type == "workflow_agent" }

// IsPhase reports whether this entry marks a phase boundary.
func (e WorkflowProgressEntry) IsPhase() bool { return e.Type == "workflow_phase" }

// WorkflowPhase names a phase declared by a workflow script.
type WorkflowPhase struct {
	Title string `json:"title"`
}

// WorkflowSnapshot is the final state of a workflow run, read from its
// on-disk manifest. The CLI writes the manifest only when the run ends, so a
// snapshot's Status is terminal in practice. Result is the workflow's return
// value (a JSON string or object).
type WorkflowSnapshot struct {
	RunID          string                  `json:"runId"`
	TaskID         string                  `json:"taskId"`
	WorkflowName   string                  `json:"workflowName"`
	Status         string                  `json:"status"` // "running","completed","stopped","killed","error"
	Result         json.RawMessage         `json:"result"`
	AgentCount     int                     `json:"agentCount"`
	DurationMs     int                     `json:"durationMs"`
	StartTime      int64                   `json:"startTime"`
	Timestamp      string                  `json:"timestamp"`
	TotalTokens    int                     `json:"totalTokens"`
	TotalToolCalls int                     `json:"totalToolCalls"`
	DefaultModel   string                  `json:"defaultModel"`
	ScriptPath     string                  `json:"scriptPath"`
	Phases         []WorkflowPhase         `json:"phases"`
	Progress       []WorkflowProgressEntry `json:"workflowProgress"`

	// Raw is the full manifest JSON, preserved for forward compatibility.
	Raw json.RawMessage `json:"-"`
}

// IsTerminal reports whether the run has reached a final status and will not
// progress further.
func (s *WorkflowSnapshot) IsTerminal() bool { return workflowStatusTerminal(s.Status) }

// Agents returns only the subagent entries from Progress.
func (s *WorkflowSnapshot) Agents() []WorkflowProgressEntry {
	var out []WorkflowProgressEntry
	for _, e := range s.Progress {
		if e.IsAgent() {
			out = append(out, e)
		}
	}
	return out
}

func workflowStatusTerminal(status string) bool {
	switch status {
	case "completed", "stopped", "killed", "failed", "error":
		return true
	default:
		return false
	}
}

func parseWorkflowSnapshot(data []byte) (*WorkflowSnapshot, error) {
	var s WorkflowSnapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse workflow manifest: %w", err)
	}
	s.Raw = append(json.RawMessage(nil), data...)
	return &s, nil
}

// ErrNoManifestPath is returned when a workflow's manifest path cannot be
// derived from a WorkflowLaunch (missing runId / script path).
var ErrNoManifestPath = errors.New("claudecli: cannot derive workflow manifest path")

// ReadWorkflowSnapshot reads and parses the workflow's manifest. Use it to
// fetch the final Result and totals after the run has ended. While the run is
// live the manifest does not exist, and the error wraps fs.ErrNotExist; for
// live state use ReadWorkflowJournal and ReadWorkflowAgentTranscript.
func ReadWorkflowSnapshot(launch *WorkflowLaunch) (*WorkflowSnapshot, error) {
	path := launch.ManifestPath()
	if path == "" {
		return nil, ErrNoManifestPath
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read workflow manifest: %w", err)
	}
	return parseWorkflowSnapshot(data)
}

// defaultWorkflowPollInterval is the manifest poll cadence used by
// WatchWorkflow when WithPollInterval is not supplied.
const defaultWorkflowPollInterval = 500 * time.Millisecond

// WatchOption configures WatchWorkflow.
//
// Deprecated: only WatchWorkflow uses it.
type WatchOption func(*watchConfig)

type watchConfig struct {
	interval time.Duration
}

// WithPollInterval sets how often WatchWorkflow re-reads the manifest.
// Values <= 0 are ignored. Defaults to 500ms.
//
// Deprecated: only WatchWorkflow uses it.
func WithPollInterval(d time.Duration) WatchOption {
	return func(c *watchConfig) {
		if d > 0 {
			c.interval = d
		}
	}
}

// WatchWorkflow polls a launched workflow's on-disk manifest and sends a
// WorkflowSnapshot whenever its contents change, until the run reaches a
// terminal status or ctx is cancelled. The returned channel is closed when
// watching ends.
//
// The CLI writes the manifest only when the run ends, so in practice the
// channel stays silent for the whole run and then delivers one terminal
// snapshot. It returns an error only when the manifest path cannot be
// derived; read and parse failures are retried.
//
// Deprecated: WatchWorkflow does not show a live run. To follow one, poll
// ReadWorkflowJournal and ReadWorkflowAgentTranscript with the offsets they
// return; to wait for the end, watch the stream for the workflow's
// task_notification and then call ReadWorkflowSnapshot.
func WatchWorkflow(ctx context.Context, launch *WorkflowLaunch, opts ...WatchOption) (<-chan WorkflowSnapshot, error) {
	path := launch.ManifestPath()
	if path == "" {
		return nil, ErrNoManifestPath
	}
	cfg := watchConfig{interval: defaultWorkflowPollInterval}
	for _, opt := range opts {
		opt(&cfg)
	}

	ch := make(chan WorkflowSnapshot)
	go func() {
		defer close(ch)
		ticker := time.NewTicker(cfg.interval)
		defer ticker.Stop()

		var last []byte
		// poll reads the manifest and emits a snapshot when it changed.
		// Returns true when watching should stop (terminal status or ctx done).
		poll := func() bool {
			data, err := os.ReadFile(path)
			if err != nil || bytes.Equal(data, last) {
				return false // not yet written / transient / unchanged
			}
			snap, err := parseWorkflowSnapshot(data)
			if err != nil {
				return false // partial write; retry on next tick
			}
			last = data
			select {
			case ch <- *snap:
			case <-ctx.Done():
				return true
			}
			return snap.IsTerminal()
		}

		if poll() {
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if poll() {
					return
				}
			}
		}
	}()
	return ch, nil
}
