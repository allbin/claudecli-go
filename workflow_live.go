package claudecli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// This file reads the on-disk state a dynamic workflow writes WHILE it runs:
// the per-run journal and each agent's transcript and meta file, all under
// WorkflowLaunch.TranscriptDir (<session>/subagents/workflows/<runId>/). The
// run manifest (ManifestPath) is written only when the run ends, so these
// files are the only live out-of-band source.
//
// The layout is undocumented CLI internals (observed on CLI 2.1.270), so every
// reader here fails soft: unknown record types are skipped, a malformed line
// is skipped, and a partially written trailing line is left for the next read.

// ErrInvalidAgentID is returned when a workflow agent id is not a plain
// lowercase alphanumeric token. The id is joined into a file path, and callers
// usually take it from an agent-influenced stream or journal, so anything that
// could traverse directories is refused before touching the filesystem.
var ErrInvalidAgentID = errors.New("claudecli: invalid workflow agent id")

// ErrNoTranscriptDir is returned when a WorkflowLaunch carries neither
// TranscriptDir nor a ScriptPath/RunID pair to derive it from.
var ErrNoTranscriptDir = errors.New("claudecli: cannot derive workflow transcript dir")

// ErrOffsetBeyondEOF is returned by the offset-based readers when the offset
// lies past the end of the file. The CLI only appends to these files, so this
// means the file was replaced or truncated; read again from offset 0.
var ErrOffsetBeyondEOF = errors.New("claudecli: read offset beyond end of file")

var workflowAgentIDPattern = regexp.MustCompile(`^[a-z0-9]+$`)

// validWorkflowAgentID reports whether id is safe to join into a path.
func validWorkflowAgentID(id string) bool { return workflowAgentIDPattern.MatchString(id) }

// transcriptDir returns the run's transcript directory, preferring the
// CLI-reported TranscriptDir and falling back to a ScriptPath derivation.
func (l *WorkflowLaunch) transcriptDir() string {
	if l == nil {
		return ""
	}
	if l.TranscriptDir != "" {
		return l.TranscriptDir
	}
	if l.ScriptPath != "" && l.RunID != "" {
		session := filepath.Dir(filepath.Dir(filepath.Dir(l.ScriptPath)))
		return filepath.Join(session, "subagents", "workflows", l.RunID)
	}
	return ""
}

// agentFilePath joins agent-<agentID><suffix> onto the transcript dir after
// validating agentID.
func (l *WorkflowLaunch) agentFilePath(agentID, suffix string) (string, error) {
	if !validWorkflowAgentID(agentID) {
		return "", fmt.Errorf("%w: %q", ErrInvalidAgentID, agentID)
	}
	dir := l.transcriptDir()
	if dir == "" {
		return "", ErrNoTranscriptDir
	}
	return filepath.Join(dir, "agent-"+agentID+suffix), nil
}

// AgentTranscriptPath returns the path to a workflow agent's live transcript
// (<transcriptDir>/agent-<agentID>.jsonl). agentID must match [a-z0-9]+;
// otherwise ErrInvalidAgentID is returned and no path is built.
func (l *WorkflowLaunch) AgentTranscriptPath(agentID string) (string, error) {
	return l.agentFilePath(agentID, ".jsonl")
}

// AgentMetaPath returns the path to a workflow agent's meta file
// (<transcriptDir>/agent-<agentID>.meta.json). agentID must match [a-z0-9]+.
func (l *WorkflowLaunch) AgentMetaPath(agentID string) (string, error) {
	return l.agentFilePath(agentID, ".meta.json")
}

// readJSONLFrom returns the complete JSONL records in path from offset on,
// and the offset just past the last record it consumed. Passing that offset
// back reads only what was appended since.
//
// A trailing segment without a newline is consumed only when it is already a
// complete JSON value; otherwise it is a write in progress and is left for the
// next call. Records are JSON objects and a truncated object is never valid,
// so this cannot mistake a half-written record for a whole one. Blank lines
// are skipped. Lines that are newline-terminated but not valid JSON are
// skipped and consumed, so one bad record cannot wedge a poller.
func readJSONLFrom(path string, offset int64) ([][]byte, int64, error) {
	if offset < 0 {
		offset = 0
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, offset, err
	}
	if offset > info.Size() {
		return nil, offset, ErrOffsetBeyondEOF
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, offset, err
	}

	var lines [][]byte
	next := offset
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		var line []byte
		if i < 0 {
			line = bytes.TrimSpace(data)
			if len(line) > 0 && (line[0] != '{' || !json.Valid(line)) {
				break // write in progress
			}
			next += int64(len(data))
			data = nil
		} else {
			line = bytes.TrimSpace(data[:i])
			next += int64(i + 1)
			data = data[i+1:]
		}
		if len(line) == 0 || !json.Valid(line) {
			continue
		}
		lines = append(lines, line)
	}
	return lines, next, nil
}

// WorkflowJournalEntry is one record of a workflow run's journal
// (WorkflowLaunch.JournalPath), which the CLI appends to while the run is
// live. Observed types on CLI 2.1.270:
//
//   - "launched": the run started. No other fields.
//   - "started": an agent started. Key, AgentID, Label and Phase are set.
//     Phase is empty when the workflow declares no phases.
//   - "result": an agent finished. Key, AgentID and Result are set.
//
// Key is the CLI's per-agent cache key, stable across a resume; AgentID names
// the agent's transcript (see ReadWorkflowAgentTranscript). Other types may
// appear in later CLIs; they decode with only Type and Raw set.
type WorkflowJournalEntry struct {
	Type    string          `json:"type"`
	Key     string          `json:"key,omitempty"`
	AgentID string          `json:"agentId,omitempty"`
	Label   string          `json:"label,omitempty"`
	Phase   string          `json:"phase,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`

	// Raw is the full journal line, preserved for forward compatibility.
	Raw json.RawMessage `json:"-"`
}

// ReadWorkflowJournal reads the run's journal from byte offset on and returns
// the complete entries plus the offset to pass next time. Start at 0; to poll,
// call again with the returned offset. A partially written last line is not
// returned and not consumed, so polling mid-write is safe.
//
// A journal that does not exist yet yields an error wrapping fs.ErrNotExist.
// Lines that are not JSON objects or have no "type" are skipped.
func ReadWorkflowJournal(launch *WorkflowLaunch, offset int64) ([]WorkflowJournalEntry, int64, error) {
	path := launch.JournalPath()
	if path == "" {
		return nil, offset, ErrNoTranscriptDir
	}
	lines, next, err := readJSONLFrom(path, offset)
	if err != nil {
		return nil, next, fmt.Errorf("read workflow journal: %w", err)
	}
	entries := make([]WorkflowJournalEntry, 0, len(lines))
	for _, line := range lines {
		var e WorkflowJournalEntry
		if json.Unmarshal(line, &e) != nil || e.Type == "" {
			continue
		}
		e.Raw = append(json.RawMessage(nil), line...)
		entries = append(entries, e)
	}
	return entries, next, nil
}

// WorkflowAgentMeta is a workflow agent's meta file
// (WorkflowLaunch.AgentMetaPath). The CLI writes it when the agent starts.
// Fields beyond AgentType and Description are optional and were absent on
// some observed runs; Raw keeps the whole file.
type WorkflowAgentMeta struct {
	AgentType           string `json:"agentType"`     // "workflow-subagent"
	Description         string `json:"description"`   // the agent's label
	WorkflowPhase       string `json:"workflowPhase"` // phase title; "" without phases
	Model               string `json:"model,omitempty"`
	WorktreePath        string `json:"worktreePath,omitempty"`
	SpawnedWithWorktree bool   `json:"spawnedWithWorktree,omitempty"`

	Raw json.RawMessage `json:"-"`
}

// ReadWorkflowAgentMeta reads and parses a workflow agent's meta file.
// agentID must match [a-z0-9]+ (ErrInvalidAgentID otherwise). A missing file
// yields an error wrapping fs.ErrNotExist.
func ReadWorkflowAgentMeta(launch *WorkflowLaunch, agentID string) (*WorkflowAgentMeta, error) {
	path, err := launch.AgentMetaPath(agentID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read workflow agent meta: %w", err)
	}
	var m WorkflowAgentMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse workflow agent meta: %w", err)
	}
	m.Raw = append(json.RawMessage(nil), data...)
	return &m, nil
}

// WorkflowTranscriptEvent is one typed event decoded from a workflow agent's
// transcript, with the metadata of the transcript line it came from.
//
// Event is one of:
//   - *TextEvent, *ThinkingEvent, *ToolUseEvent from assistant lines (Model is
//     set from the message; ParentToolUseID is empty).
//   - *ToolResultEvent from a user line's tool_result blocks.
//   - *UserEvent from a user line carrying text, such as the agent's prompt.
//
// One transcript line can yield several events; they share UUID and Timestamp.
type WorkflowTranscriptEvent struct {
	// UUID is the transcript line's uuid.
	UUID string
	// Timestamp is when the CLI wrote the line; zero if absent or unparsable.
	Timestamp time.Time
	Event     Event
}

// rawTranscriptLine is the subset of Claude Code's session JSONL format that
// ReadWorkflowAgentTranscript decodes.
type rawTranscriptLine struct {
	Type              string      `json:"type"`
	UUID              string      `json:"uuid"`
	Timestamp         string      `json:"timestamp"`
	SessionID         string      `json:"sessionId"`
	IsAPIErrorMessage bool        `json:"isApiErrorMessage"`
	Message           *rawMessage `json:"message"`
}

// ReadWorkflowAgentTranscript reads a workflow agent's transcript
// (agent-<agentID>.jsonl) from byte offset on, decodes it into typed events,
// and returns the offset to pass next time. Like ReadWorkflowJournal it leaves
// a partially written last line for the next call.
//
// The transcript uses Claude Code's session JSONL format, not the stream-json
// wire format. Only "assistant" and "user" lines are decoded; attachments,
// summaries and any other line types are skipped, as are unknown content
// blocks. agentID must match [a-z0-9]+ (ErrInvalidAgentID otherwise).
func ReadWorkflowAgentTranscript(launch *WorkflowLaunch, agentID string, offset int64) ([]WorkflowTranscriptEvent, int64, error) {
	path, err := launch.AgentTranscriptPath(agentID)
	if err != nil {
		return nil, offset, err
	}
	lines, next, err := readJSONLFrom(path, offset)
	if err != nil {
		return nil, next, fmt.Errorf("read workflow agent transcript: %w", err)
	}
	var out []WorkflowTranscriptEvent
	for _, line := range lines {
		out = appendTranscriptLine(out, line)
	}
	return out, next, nil
}

// appendTranscriptLine decodes one session-JSONL line and appends its events.
func appendTranscriptLine(out []WorkflowTranscriptEvent, line []byte) []WorkflowTranscriptEvent {
	var raw rawTranscriptLine
	if json.Unmarshal(line, &raw) != nil || raw.Message == nil {
		return out
	}
	ts, _ := time.Parse(time.RFC3339Nano, raw.Timestamp)
	add := func(ev Event) {
		out = append(out, WorkflowTranscriptEvent{UUID: raw.UUID, Timestamp: ts, Event: ev})
	}

	switch raw.Type {
	case "assistant":
		meta := assistantMeta{Model: raw.Message.Model}
		var discard []string
		for _, block := range raw.Message.Content {
			parseContentBlock(block, meta, &discard, func(ev Event) {
				if _, unknown := ev.(*UnknownEvent); !unknown {
					add(ev)
				}
			})
		}

	case "user":
		var texts []UserContent
		for _, block := range raw.Message.Content {
			switch block.Type {
			case "tool_result":
				add(&ToolResultEvent{
					ToolUseID: block.ToolUseID,
					Content:   extractContent(block.Content),
				})
			case "text":
				texts = append(texts, UserContent{Type: "text", Text: block.Text})
			}
		}
		if len(texts) > 0 {
			add(&UserEvent{
				Content:   texts,
				SessionID: raw.SessionID,
				UUID:      raw.UUID,
				Timestamp: raw.Timestamp,
			})
		}
	}
	return out
}
