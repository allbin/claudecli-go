package claudecli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const defaultControlTimeout = 30 * time.Second
const defaultInitTimeout = 60 * time.Second
const defaultToolProgressInterval = 30 * time.Second

// Session represents a long-lived interactive Claude CLI session with
// bidirectional control protocol support.
//
// Create via Client.Connect(). Send messages with Query().
// Read events from Events(). Close when done.
type Session struct {
	proc   *Process
	events chan Event
	done   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc

	// stdin writer (protected by mu)
	mu          sync.Mutex
	stdinClosed bool // set on Close() or write failure

	// control protocol state
	reqCounter     atomic.Int64
	pending        sync.Map // map[string]chan controlResult
	controlTimeout time.Duration
	initTimeout    time.Duration
	controlWg      sync.WaitGroup // tracks in-flight handleControlRequest goroutines
	pump           chan Event     // set by readLoop; sendEvent writes here for ordering
	pumpClosed     chan struct{}  // closed by readLoop defer BEFORE close(pump) — sendEvent uses to avoid writing to a closed pump

	// callbacks
	canUseTool ToolPermissionFunc
	userInput  UserInputFunc

	// state tracking
	sessionID       string
	serverInfo      json.RawMessage
	stateMu         sync.Mutex
	state           State
	result          *ResultEvent
	err             error
	waited          bool
	resultReady     chan struct{} // closed when a ResultEvent or fatal error is tracked
	resultCloseOnce sync.Once
	readyCh         chan struct{} // closed after initialize (or first system event on older CLIs)
	readyOnce       sync.Once
	activity        *activityTracker // guarded by stateMu
	lastStdoutAt    atomic.Int64     // unix nanos of last stdout line; 0 until first line

	// ToolProgressEvent ticker. toolProgressStop is accessed only from the
	// readLoop goroutine (start/stop are driven by transition observations,
	// not user calls). Interval override is atomic so tests can adjust it
	// between Connect() and the first tool_use.
	toolProgressStop         chan struct{}
	toolProgressIntervalNs   atomic.Int64
}

// ProcessInfo reports process-level state for watchdogs and health monitoring.
// LastStdoutAt is updated from the stdout scanner loop and is independent of
// parsed events, so a stall can be distinguished from a quiet turn.
type ProcessInfo struct {
	// LastStdoutAt is the time the CLI last wrote a line to stdout. Zero
	// until the first line is received.
	LastStdoutAt time.Time
	// ActivityState is the derived activity state (idle, thinking, awaiting_tool_result).
	ActivityState ActivityState
	// Lifecycle is the session lifecycle state.
	Lifecycle State
	// SessionID is the CLI-assigned session ID, or empty if not yet assigned.
	SessionID string
}

// ProcessInfo returns a snapshot of process-level state useful for watchdogs.
// Consumers can compare LastStdoutAt against the wall clock to detect stdout
// stalls without having to infer state from event pairings.
func (s *Session) ProcessInfo() ProcessInfo {
	nanos := s.lastStdoutAt.Load()
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	var last time.Time
	if nanos != 0 {
		last = time.Unix(0, nanos)
	}
	return ProcessInfo{
		LastStdoutAt:  last,
		ActivityState: s.activity.State(),
		Lifecycle:     s.state,
		SessionID:     s.sessionID,
	}
}

// ActivityState returns the current activity state.
func (s *Session) ActivityState() ActivityState {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.activity.State()
}

// Events returns the event channel. Closed when session ends.
// Control requests are handled internally and not exposed here.
func (s *Session) Events() <-chan Event { return s.events }

// State returns the current lifecycle state.
func (s *Session) State() State {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.state
}

// SessionID returns the session ID assigned by the CLI.
func (s *Session) SessionID() string {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.sessionID
}

// prepareQuery validates state and transitions to StateRunning.
// Must be called before sending any query.
func (s *Session) prepareQuery() error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	switch s.state {
	case StateFailed:
		return fmt.Errorf("session failed: %w", s.err)
	case StateRunning:
		return fmt.Errorf("query already in progress")
	case StateDone:
		return fmt.Errorf("session ended")
	case StateStarting:
		return fmt.Errorf("session not ready")
	}
	// Valid: StateIdle
	s.state = StateRunning
	s.waited = false
	s.result = nil
	s.err = nil
	s.resultReady = make(chan struct{})
	s.resultCloseOnce = sync.Once{}
	return nil
}

// validateSendable checks that the session can accept a message.
// Unlike prepareQuery, it allows StateRunning (for mid-turn injection).
func (s *Session) validateSendable() error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	switch s.state {
	case StateFailed:
		return fmt.Errorf("session failed: %w", s.err)
	case StateDone:
		return fmt.Errorf("session ended")
	case StateStarting:
		return fmt.Errorf("session not ready")
	}
	return nil
}

// sendUserMessage marshals and writes a user message with the given content.
func (s *Session) sendUserMessage(content any) error {
	s.stateMu.Lock()
	sid := s.sessionID
	s.stateMu.Unlock()
	msg := userMessage{
		Type:            "user",
		SessionID:       sid,
		Message:         messageBody{Role: "user", Content: content},
		ParentToolUseID: nil,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal user message: %w", err)
	}
	return s.writeStdin(append(data, '\n'))
}

// Query sends a user message to the CLI.
func (s *Session) Query(prompt string) error {
	if err := s.prepareQuery(); err != nil {
		return err
	}
	// Emit thinking transition before writing stdin so the transition is
	// visible in the pump ahead of any CLI response to this query.
	s.emitQueryActivity()
	return s.sendUserMessage(prompt)
}

// QueryWithContent sends a user message with multimodal content blocks.
// The prompt is prepended as a text block, followed by the provided blocks.
func (s *Session) QueryWithContent(prompt string, blocks ...ContentBlock) error {
	if err := s.prepareQuery(); err != nil {
		return err
	}
	content := make([]ContentBlock, 0, 1+len(blocks))
	content = append(content, TextBlock(prompt))
	content = append(content, blocks...)
	s.emitQueryActivity()
	return s.sendUserMessage(content)
}

// SendMessage sends a user message without result tracking.
// Unlike Query, it can be called while another query is in progress,
// allowing mid-turn message injection. The CLI folds injected messages
// into the current turn's result.
func (s *Session) SendMessage(prompt string) error {
	if err := s.validateSendable(); err != nil {
		return err
	}
	s.emitQueryActivity()
	return s.sendUserMessage(prompt)
}

// SendMessageWithContent sends a multimodal user message without result tracking.
// See SendMessage for usage details.
func (s *Session) SendMessageWithContent(prompt string, blocks ...ContentBlock) error {
	if err := s.validateSendable(); err != nil {
		return err
	}
	content := make([]ContentBlock, 0, 1+len(blocks))
	content = append(content, TextBlock(prompt))
	content = append(content, blocks...)
	s.emitQueryActivity()
	return s.sendUserMessage(content)
}

// emitQueryActivity pushes a CLIStateChangeEvent(thinking) to the event
// stream when the tracker is idle. No-op when already in a non-idle state
// (e.g. mid-turn SendMessage injection).
func (s *Session) emitQueryActivity() {
	s.stateMu.Lock()
	transition := s.activity.markQuery()
	s.stateMu.Unlock()
	if transition != nil {
		s.sendEvent(transition)
	}
}

// Wait blocks until a ResultEvent or error for the current query.
// In multi-turn sessions, returns after each result (not at process exit).
// Idempotent within a single query: multiple calls return the same result.
// Safe to call concurrently with Events() -- Wait does not consume events.
func (s *Session) Wait() (*ResultEvent, error) {
	s.stateMu.Lock()
	if s.waited {
		result, err := s.result, s.err
		s.stateMu.Unlock()
		return result, err
	}
	ready := s.resultReady
	s.stateMu.Unlock()

	select {
	case <-ready:
	case <-s.done:
	}

	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.waited = true
	return s.result, s.err
}

// Interrupt sends an interrupt to the CLI.
func (s *Session) Interrupt() error {
	return s.sendControlRequest("interrupt", nil)
}

// SetPermissionMode changes the permission mode mid-session.
func (s *Session) SetPermissionMode(mode PermissionMode) error {
	return s.sendControlRequest("set_permission_mode", map[string]any{"mode": string(mode)})
}

// SetModel changes the model mid-session.
func (s *Session) SetModel(model Model) error {
	return s.sendControlRequest("set_model", map[string]any{"model": string(model)})
}

// GetServerInfo returns the raw JSON from the initialize response.
func (s *Session) GetServerInfo() json.RawMessage {
	return s.serverInfo
}

// RewindFiles rewinds files to a previous checkpoint.
func (s *Session) RewindFiles(userMessageID string) error {
	return s.sendControlRequest("rewind_files", map[string]any{"user_message_id": userMessageID})
}

// ReconnectMCPServer reconnects a named MCP server.
func (s *Session) ReconnectMCPServer(serverName string) error {
	return s.sendControlRequest("mcp_reconnect", map[string]any{"serverName": serverName})
}

// ToggleMCPServer enables or disables a named MCP server.
func (s *Session) ToggleMCPServer(serverName string, enabled bool) error {
	return s.sendControlRequest("mcp_toggle", map[string]any{
		"serverName": serverName,
		"enabled":     enabled,
	})
}

// StopTask stops a running task by ID.
func (s *Session) StopTask(taskID string) error {
	return s.sendControlRequest("stop_task", map[string]any{"task_id": taskID})
}

// GetMCPStatus queries MCP server connection status.
func (s *Session) GetMCPStatus() error {
	return s.sendControlRequest("mcp_status", nil)
}

// QueryMCPStatus queries MCP server connection status and returns the parsed result.
func (s *Session) QueryMCPStatus() ([]MCPServerStatus, error) {
	resp, err := s.sendControlRequestRaw("mcp_status", nil)
	if err != nil {
		return nil, err
	}
	var wrapper struct {
		MCPServers []MCPServerStatus `json:"mcpServers"`
	}
	if err := json.Unmarshal(resp, &wrapper); err == nil && wrapper.MCPServers != nil {
		return wrapper.MCPServers, nil
	}
	// Fall back to bare array (CLI < v2.1.97).
	var servers []MCPServerStatus
	if err := json.Unmarshal(resp, &servers); err != nil {
		return nil, fmt.Errorf("parse mcp_status response: %w", err)
	}
	return servers, nil
}

// ReconnectMCPServerWait reconnects a named MCP server and blocks until it
// reports connected status. A zero timeout uses the default (10s).
func (s *Session) ReconnectMCPServerWait(serverName string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	if err := s.ReconnectMCPServer(serverName); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(s.ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		servers, err := s.QueryMCPStatus()
		if err != nil {
			return fmt.Errorf("mcp_reconnect_wait: status query: %w", err)
		}
		for _, srv := range servers {
			if srv.Name == serverName && srv.Status == "connected" {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			if s.ctx.Err() != nil {
				return s.ctx.Err()
			}
			return fmt.Errorf("mcp_reconnect_wait: %s not connected after %s", serverName, timeout)
		case <-ticker.C:
		}
	}
}

// Close terminates the session. Closes stdin (EOF signal) and waits up to
// 5 seconds for the CLI to exit gracefully before canceling the context
// (SIGTERM). The grace period prevents interrupting session file writes
// which can lose the last assistant message.
func (s *Session) Close() error {
	s.mu.Lock()
	s.stdinClosed = true
	if s.proc.Stdin != nil {
		s.proc.Stdin.Close()
	}
	s.mu.Unlock()

	// Give the CLI time to flush after stdin EOF before sending SIGTERM.
	select {
	case <-s.done:
		// Process exited gracefully within the grace period.
	case <-time.After(5 * time.Second):
		// Grace period expired — force terminate.
		s.cancel()
	}

	for range s.events {
	}
	<-s.done
	return nil
}

// writeStdin writes data to the CLI's stdin, protected by mutex.
func (s *Session) writeStdin(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stdinClosed {
		return fmt.Errorf("session closed")
	}
	if s.proc.Stdin == nil {
		return fmt.Errorf("stdin closed")
	}
	_, err := s.proc.Stdin.Write(data)
	if err != nil {
		s.stdinClosed = true
	}
	return err
}

// sendControlRequest sends a control request and waits for the CLI's response.
func (s *Session) sendControlRequest(subtype string, data map[string]any) error {
	_, err := s.sendControlRequestRaw(subtype, data)
	return err
}

// sendControlRequestRaw sends a control request and returns the raw response body.
func (s *Session) sendControlRequestRaw(subtype string, data map[string]any) (json.RawMessage, error) {
	id := fmt.Sprintf("req_%d", s.reqCounter.Add(1))
	resultCh := make(chan controlResult, 1)
	s.pending.Store(id, resultCh)
	defer s.pending.Delete(id)

	reqMap := make(map[string]any, len(data)+1)
	maps.Copy(reqMap, data)
	reqMap["subtype"] = subtype

	payload := map[string]any{
		"type":       "control_request",
		"request_id": id,
		"request":    reqMap,
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if err := s.writeStdin(append(raw, '\n')); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(s.ctx, s.controlTimeout)
	defer cancel()

	select {
	case result := <-resultCh:
		if result.Err != nil {
			return nil, fmt.Errorf("%s: %w", subtype, result.Err)
		}
		return result.Response, nil
	case <-ctx.Done():
		if s.ctx.Err() != nil {
			return nil, s.ctx.Err()
		}
		return nil, fmt.Errorf("%s: timeout after %s", subtype, s.controlTimeout)
	}
}

// initialize sends the initialize control request and waits for response.
// Uses initTimeout (default 60s) rather than controlTimeout because the CLI
// may need extra time to connect to MCP servers during startup.
func (s *Session) initialize() error {
	id := fmt.Sprintf("req_%d", s.reqCounter.Add(1))
	resultCh := make(chan controlResult, 1)
	s.pending.Store(id, resultCh)
	defer s.pending.Delete(id)

	payload := map[string]any{
		"type":       "control_request",
		"request_id": id,
		"request": map[string]any{
			"subtype": "initialize",
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal initialize: %w", err)
	}
	if err := s.writeStdin(append(raw, '\n')); err != nil {
		return fmt.Errorf("write initialize: %w", err)
	}

	ctx, cancel := context.WithTimeout(s.ctx, s.initTimeout)
	defer cancel()

	select {
	case result := <-resultCh:
		if result.Err != nil {
			return fmt.Errorf("initialize: %w", result.Err)
		}
		s.serverInfo = result.Response
		return nil
	case <-ctx.Done():
		if s.ctx.Err() != nil {
			return s.ctx.Err()
		}
		return fmt.Errorf("initialize: timeout after %s", s.initTimeout)
	}
}

// readLoop reads stdout, routes control messages, forwards events.
//
// An internal pump goroutine decouples stdout reading from event delivery:
// stdout parsing writes to a buffered internal channel, and the pump drains
// it into s.events. This prevents a slow event consumer from blocking the
// stdout scanner, which would stall control response processing.
func (s *Session) readLoop() {
	defer close(s.done)
	defer s.setDoneState()
	defer s.failPendingRequests()

	// Event pump: buffered intermediary so stdout reading never blocks
	// on a slow event consumer. Stored on session so sendEvent (called
	// from handleControlRequest goroutines) writes through the pump
	// rather than directly to s.events, preserving event ordering.
	pump := make(chan Event, 256)
	s.pump = pump
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		for ev := range pump {
			select {
			case s.events <- ev:
			case <-s.ctx.Done():
			}
		}
	}()
	// exitEv is set near the end of readLoop; the deferred close emits it
	// AFTER pump drains (so it is the last event observable on s.events)
	// but BEFORE close(s.events). Direct send bypasses the pump's
	// ctx.Done early-out, so context-canceled exits still surface a reason.
	var exitEv *CLIExitEvent
	defer func() {
		close(s.pumpClosed)
		close(pump)
		<-pumpDone
		if exitEv != nil {
			// Non-blocking first: succeeds immediately when the consumer
			// is draining (the common case on a 64-buffer channel).
			// Falls back to a short blocking wait so a momentarily-slow
			// consumer still sees the event without a long Close() stall
			// when one has truly abandoned the channel.
			select {
			case s.events <- exitEv:
			default:
				timer := time.NewTimer(250 * time.Millisecond)
				select {
				case s.events <- exitEv:
				case <-timer.C:
				}
				timer.Stop()
			}
		}
		close(s.events)
	}()
	// Defer order (LIFO): stopToolProgressTicker runs before the pump is
	// closed so the ticker goroutine exits before pump writes would drop.
	defer s.stopToolProgressTicker()

	pumpSendRaw := func(ev Event) {
		select {
		case pump <- ev:
		case <-s.ctx.Done():
		}
	}
	// pumpSend emits a CLIStateChangeEvent BEFORE ev when the tracker
	// detects a transition, so consumers see state changes ahead of the
	// event that triggered them. Transitions into/out of
	// ActivityAwaitingToolResult also start/stop the ToolProgressEvent
	// ticker here so its lifetime mirrors the state machine exactly.
	pumpSend := func(ev Event) {
		s.stateMu.Lock()
		transition := s.activity.observe(ev)
		s.stateMu.Unlock()
		if transition != nil {
			pumpSendRaw(transition)
			if transition.State == ActivityAwaitingToolResult {
				s.startToolProgressTicker()
			} else {
				s.stopToolProgressTicker()
			}
		}
		pumpSendRaw(ev)
	}

	stderrRing, stderrDone := scanStderr(s.ctx, s.proc, pump, nil)

	// Capture raw stdout JSONL lines for diagnostics on error exit.
	stdoutRing := newStderrRing(10)
	stdoutCapture := &lineCaptureReader{r: s.proc.Stdout, ring: stdoutRing}

	scanner := bufio.NewScanner(stdoutCapture)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	var resultText []string
	var snapshot *ContextSnapshot
	var lastModel string
	var lastStdoutErr error
	var unknowns []*UnknownEvent
	var turnCounter int

	for scanner.Scan() {
		line := scanner.Bytes()
		s.lastStdoutAt.Store(time.Now().UnixNano())
		if len(line) == 0 {
			continue
		}

		var raw rawEvent
		if err := json.Unmarshal(line, &raw); err != nil {
			pumpSend(&ErrorEvent{Err: fmt.Errorf("unmarshal JSONL: %w", err)})
			continue
		}

		switch raw.Type {
		case "control_response":
			s.handleControlResponse(line)

		case "control_request":
			s.controlWg.Add(1)
			go func() {
				defer s.controlWg.Done()
				s.handleControlRequest(raw.RequestID, raw.Request)
			}()

		case "system":
			switch raw.Subtype {
			case "init", "":
				resultText = nil
				snapshot = nil
				lastModel = ""
				ev := &InitEvent{
					SessionID:  raw.SessionID,
					Model:      raw.Model,
					Tools:      raw.Tools,
					Agents:     raw.Agents,
					Skills:     raw.Skills,
					MCPServers: raw.MCPServers,
				}
				s.stateMu.Lock()
				s.sessionID = raw.SessionID
				s.stateMu.Unlock()
				s.trackState(ev)
				s.readyOnce.Do(func() { close(s.readyCh) })
				pumpSend(ev)
			case "status":
				status := ""
				if raw.Status != nil {
					status = *raw.Status
				}
				pumpSend(&CompactStatusEvent{
					SessionID: raw.SessionID,
					Status:    status,
				})
			case "compact_boundary":
				pumpSend(parseCompactBoundaryEvent(&raw))
			case "task_started", "task_progress", "task_notification":
				pumpSend(parseTaskEvent(&raw, line))
			case "hook_started", "hook_response":
				pumpSend(parseHookEvent(&raw, line))
			default:
				pumpSend(&UnknownEvent{
					Type: "system/" + raw.Subtype,
					Raw:  append(json.RawMessage(nil), line...),
				})
			}

		case "assistant":
			if raw.Message == nil {
				continue
			}
			parentToolUseID := ""
			if raw.ParentToolUseID != nil {
				parentToolUseID = *raw.ParentToolUseID
			}
			// Emit TurnEvent for top-level assistant messages, matching
			// ParseEvents behaviour so Session consumers see the same
			// turn boundaries as stateless-parse consumers.
			if parentToolUseID == "" {
				turnCounter++
				toolName := ""
				for _, block := range raw.Message.Content {
					if block.Type == "tool_use" {
						toolName = block.Name
						break
					}
				}
				pumpSend(&TurnEvent{Turn: turnCounter, ToolName: toolName})
			}
			for _, block := range raw.Message.Content {
				switch block.Type {
				case "thinking":
					pumpSend(&ThinkingEvent{Content: block.Thinking, Signature: block.Signature, ParentToolUseID: parentToolUseID})
				case "text":
					resultText = append(resultText, block.Text)
					pumpSend(&TextEvent{Content: block.Text, ParentToolUseID: parentToolUseID})
				case "tool_use":
					pumpSend(&ToolUseEvent{
						ID:              block.ID,
						Name:            block.Name,
						Input:           block.Input,
						ParentToolUseID: parentToolUseID,
					})
				case "tool_result":
					pumpSend(&ToolResultEvent{
						ToolUseID:       block.ToolUseID,
						Content:         extractContent(block.Content),
						ParentToolUseID: parentToolUseID,
					})
				default:
					if block.Type != "" {
						blockRaw, _ := json.Marshal(block)
						ev := &UnknownEvent{
							Type: "content/" + block.Type,
							Raw:  blockRaw,
						}
						unknowns = append(unknowns, ev)
						pumpSend(ev)
					}
				}
			}
			if len(raw.Message.ContextManagement) > 0 && string(raw.Message.ContextManagement) != "null" {
				pumpSend(&ContextManagementEvent{Raw: raw.Message.ContextManagement})
			}

		case "result":
			modelUsage := convertModelUsage(raw.ModelUsage)
			if snapshot != nil && lastModel != "" {
				if mu, ok := lookupModelUsage(modelUsage, lastModel); ok {
					snapshot.ContextWindow = mu.ContextWindow
				}
			}
			// Classify error_max_turns as a non-fatal ErrorEvent so
			// downstream consumers see the typed error alongside the
			// terminating ResultEvent (matches ParseEvents behaviour).
			if raw.Subtype == "error_max_turns" {
				pumpSend(&ErrorEvent{Err: classifyMaxTurns(raw.Errors), Fatal: false})
			}
			ev := &ResultEvent{
				Text:             strings.Join(resultText, ""),
				Subtype:          raw.Subtype,
				StopReason:       raw.StopReason,
				StructuredOutput: raw.StructuredOutput,
				Duration:         time.Duration(raw.DurationMS) * time.Millisecond,
				CostUSD:          raw.CostUSD,
				SessionID:        raw.SessionID,
				NumTurns:         raw.NumTurns,
				Usage:            raw.Usage.toUsage(),
				ModelUsage:       modelUsage,
				ContextSnapshot:  snapshot,
			}
			resultText = nil
			snapshot = nil
			lastModel = ""
			s.trackState(ev)
			pumpSend(ev)

		case "rate_limit_event":
			pumpSend(parseRateLimitEvent(&raw))

		case "stream_event":
			pumpSend(&StreamEvent{
				UUID:      raw.UUID,
				SessionID: raw.SessionID,
				Event:     raw.Event,
			})
			updateContextSnapshot(raw.Event, &snapshot, &lastModel)

		case "error":
			errEv := parseErrorEvent(&raw)
			if errEv.Err != nil {
				lastStdoutErr = errEv.Err
			}
			pumpSend(errEv)

		case "user":
			pumpSend(parseUserEvent(&raw))

		default:
			ev := &UnknownEvent{
				Type: raw.Type,
				Raw:  append(json.RawMessage(nil), line...),
			}
			unknowns = append(unknowns, ev)
			pumpSend(ev)
		}
	}

	if err := scanner.Err(); err != nil {
		pumpSend(&ErrorEvent{Err: fmt.Errorf("scanner: %w", err)})
	}

	// Wait for in-flight handleControlRequest goroutines before closing pump.
	s.controlWg.Wait()

	<-stderrDone

	stdoutCapture.flush()

	waitErr := s.proc.Wait()
	var exitErrForEvent error
	if waitErr != nil {
		stderr := strings.Join(stderrRing.lines(), "\n")
		cliErr := processExitError(waitErr, stderr)
		if cliErr.Message == "" && lastStdoutErr != nil {
			cliErr.Message = lastStdoutErr.Error()
			if cliErr.class == nil {
				cliErr.class = lastStdoutErr
			}
		}
		if cliErr.Message == "" && len(unknowns) > 0 {
			var msgs []string
			for _, u := range unknowns {
				msg := fmt.Sprintf("unknown event %q: %s", u.Type, string(u.Raw))
				if len(msg) > 200 {
					msg = msg[:200] + "..."
				}
				msgs = append(msgs, msg)
			}
			cliErr.Message = "unrecognized CLI events may contain error details: " + strings.Join(msgs, "; ")
		}
		cliErr.LastEvents = stdoutRing.lines()
		ev := &ErrorEvent{
			Err:   cliErr,
			Fatal: true,
		}
		s.trackState(ev)
		pumpSend(ev)
		exitErrForEvent = cliErr
	}

	exitEv = classifyExit(waitErr, s.ctx.Err(), exitErrForEvent)
}

// classifyExit maps the cmd.Wait error and context state into a structured
// CLIExitEvent. ctxErr takes priority over the wait error: if the context
// was canceled, the SDK initiated termination, so report context_canceled
// even when the kernel reports the kill via signal.
func classifyExit(waitErr error, ctxErr error, cliErr error) *CLIExitEvent {
	signal, code := extractExitDetails(waitErr)
	ev := &CLIExitEvent{
		ExitCode: code,
		Signal:   signal,
		Err:      cliErr,
		At:       time.Now(),
	}
	switch {
	case ctxErr != nil:
		ev.Reason = ExitReasonContextCanceled
		if ev.Err == nil {
			ev.Err = ctxErr
		}
	case waitErr == nil:
		ev.Reason = ExitReasonNormal
	case signal != "":
		ev.Reason = ExitReasonKilled
	case code > 0:
		ev.Reason = ExitReasonCrashed
	default:
		// Reached when waitErr is non-nil but not an *exec.ExitError
		// (e.g. IO failure, broken pipe). Not a clean process exit.
		ev.Reason = ExitReasonUnknown
	}
	return ev
}

// setDoneState transitions to StateDone when readLoop exits (process ended).
func (s *Session) setDoneState() {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.state != StateFailed {
		s.state = StateDone
	}
	s.readyOnce.Do(func() { close(s.readyCh) })
}

// failPendingRequests signals all pending control request waiters with an error.
// Called when readLoop exits to prevent waiters from hanging until timeout.
func (s *Session) failPendingRequests() {
	s.pending.Range(func(key, value any) bool {
		ch := value.(chan controlResult)
		select {
		case ch <- controlResult{Err: fmt.Errorf("session ended")}:
		default:
		}
		s.pending.Delete(key)
		return true
	})
}

// startToolProgressTicker launches the periodic ToolProgressEvent emitter.
// Called from the readLoop goroutine on transition into
// ActivityAwaitingToolResult. Idempotent — a no-op if a ticker is already
// running.
func (s *Session) startToolProgressTicker() {
	if s.toolProgressStop != nil {
		return
	}
	intervalNs := s.toolProgressIntervalNs.Load()
	interval := time.Duration(intervalNs)
	if interval <= 0 {
		interval = defaultToolProgressInterval
	}
	stop := make(chan struct{})
	s.toolProgressStop = stop
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-s.ctx.Done():
				return
			case now := <-ticker.C:
				s.emitToolProgress(now)
			}
		}
	}()
}

// stopToolProgressTicker signals the ticker goroutine to exit. Safe to call
// when no ticker is running. Called from the readLoop goroutine on
// transition out of ActivityAwaitingToolResult and on readLoop exit.
func (s *Session) stopToolProgressTicker() {
	if s.toolProgressStop == nil {
		return
	}
	close(s.toolProgressStop)
	s.toolProgressStop = nil
}

// emitToolProgress publishes a ToolProgressEvent for the current first
// pending top-level tool_use. Safe concurrency: FirstPending is read under
// stateMu; sendEvent handles pump-closed races.
func (s *Session) emitToolProgress(now time.Time) {
	s.stateMu.Lock()
	p, ok := s.activity.FirstPending()
	s.stateMu.Unlock()
	if !ok {
		return
	}
	s.sendEvent(&ToolProgressEvent{
		ToolUseID: p.ID,
		ToolName:  p.Name,
		Elapsed:   now.Sub(p.StartedAt),
		At:        now,
	})
}

func (s *Session) sendEvent(ev Event) {
	// pumpClosed is closed by readLoop's defer BEFORE close(pump), so a
	// user-goroutine caller (e.g. emitQueryActivity racing shutdown) sees
	// the close signal and drops the event instead of panicking on a
	// closed channel write.
	select {
	case <-s.pumpClosed:
		return
	default:
	}
	select {
	case s.pump <- ev:
	case <-s.pumpClosed:
	case <-s.ctx.Done():
	}
}

func (s *Session) trackState(event Event) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	switch e := event.(type) {
	case *InitEvent:
		if s.state == StateStarting {
			s.state = StateIdle
		}
	case *ResultEvent:
		s.state = StateIdle
		s.result = e
		// Match Stream.Wait behaviour: surface error_max_turns as a
		// typed error so callers can use errors.Is(err, ErrMaxTurns).
		if s.err == nil && e.Subtype == "error_max_turns" {
			s.err = &MaxTurnsError{
				Turns:   e.NumTurns,
				Message: "reached maximum number of turns",
			}
		}
		s.resultCloseOnce.Do(func() { close(s.resultReady) })
	case *ErrorEvent:
		if e.Fatal {
			s.state = StateFailed
			if s.err == nil {
				s.err = e.Err
			}
			s.resultCloseOnce.Do(func() { close(s.resultReady) })
		}
	}
}

func (s *Session) handleControlResponse(line []byte) {
	var resp struct {
		Response struct {
			RequestID string          `json:"request_id"`
			Subtype   string          `json:"subtype"`
			Response  json.RawMessage `json:"response,omitempty"`
			Error     string          `json:"error,omitempty"`
		} `json:"response"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		s.sendEvent(&ErrorEvent{Err: fmt.Errorf("unmarshal control_response: %w", err)})
		return
	}

	if ch, ok := s.pending.LoadAndDelete(resp.Response.RequestID); ok {
		resultCh := ch.(chan controlResult)
		if resp.Response.Subtype == "error" {
			resultCh <- controlResult{Err: fmt.Errorf("control error: %s", resp.Response.Error)}
		} else {
			resultCh <- controlResult{Response: resp.Response.Response}
		}
	}
}

func (s *Session) handleControlRequest(requestID string, body json.RawMessage) {
	defer func() {
		if r := recover(); r != nil {
			s.sendControlResponse(requestID, nil, fmt.Errorf("callback panic: %v", r))
		}
	}()

	var req rawControlRequestBody
	if err := json.Unmarshal(body, &req); err != nil {
		s.sendControlResponse(requestID, nil, err)
		return
	}

	switch req.Subtype {
	case "can_use_tool":
		var permReq ToolPermissionRequest
		if err := json.Unmarshal(body, &permReq); err != nil {
			s.sendControlResponse(requestID, nil, err)
			return
		}

		// Route AskUserQuestion to userInput callback when available.
		if permReq.ToolName == "AskUserQuestion" && s.userInput != nil {
			s.handleUserInput(requestID, permReq)
			return
		}

		if s.canUseTool == nil {
			s.sendControlResponse(requestID, nil, fmt.Errorf("no canUseTool callback registered"))
			return
		}

		// Run callback in sub-goroutine so context cancellation can unblock us.
		// Callbacks should return promptly or check s.ctx for cancellation.
		type callbackResult struct {
			resp *PermissionResponse
			err  error
		}
		ch := make(chan callbackResult, 1)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					ch <- callbackResult{err: fmt.Errorf("callback panic: %v", r)}
				}
			}()
			resp, err := s.canUseTool(permReq.ToolName, permReq.Input)
			ch <- callbackResult{resp, err}
		}()

		var resp *PermissionResponse
		select {
		case result := <-ch:
			if result.err != nil {
				s.sendControlResponse(requestID, nil, result.err)
				return
			}
			resp = result.resp
		case <-s.ctx.Done():
			s.sendControlResponse(requestID, nil, s.ctx.Err())
			return
		}

		if resp.Allow {
			data := map[string]any{
				"behavior":     "allow",
				"updatedInput": resp.UpdatedInput,
			}
			if resp.UpdatedInput == nil {
				data["updatedInput"] = permReq.Input
			}
			s.sendControlResponse(requestID, data, nil)
		} else {
			s.sendControlResponse(requestID, map[string]any{
				"behavior": "deny",
				"message":  resp.DenyMessage,
			}, nil)
		}

	default:
		s.sendControlResponse(requestID, nil, fmt.Errorf("unsupported control request: %s", req.Subtype))
	}
}

// handleUserInput routes AskUserQuestion requests to the userInput callback.
func (s *Session) handleUserInput(requestID string, permReq ToolPermissionRequest) {
	var input struct {
		Questions []Question `json:"questions"`
	}
	if err := json.Unmarshal(permReq.Input, &input); err != nil {
		s.sendControlResponse(requestID, nil, err)
		return
	}

	type callbackResult struct {
		answers map[string]string
		err     error
	}
	ch := make(chan callbackResult, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- callbackResult{err: fmt.Errorf("callback panic: %v", r)}
			}
		}()
		answers, err := s.userInput(input.Questions)
		ch <- callbackResult{answers, err}
	}()

	select {
	case result := <-ch:
		if result.err != nil {
			s.sendControlResponse(requestID, nil, result.err)
			return
		}
		answers := result.answers
		if answers == nil {
			answers = make(map[string]string)
		}
		s.sendControlResponse(requestID, map[string]any{
			"behavior": "allow",
			"updatedInput": map[string]any{
				"questions": input.Questions,
				"answers":   answers,
			},
		}, nil)
	case <-s.ctx.Done():
		s.sendControlResponse(requestID, nil, s.ctx.Err())
	}
}

func (s *Session) sendControlResponse(requestID string, response any, respErr error) {
	var resp rawControlResponse
	resp.Type = "control_response"
	if respErr != nil {
		resp.Response = controlResponseBody{
			Subtype:   "error",
			RequestID: requestID,
			Error:     respErr.Error(),
		}
	} else {
		resp.Response = controlResponseBody{
			Subtype:   "success",
			RequestID: requestID,
			Response:  response,
		}
	}
	data, marshalErr := json.Marshal(resp)
	if marshalErr != nil {
		// Marshal failed — send hardcoded error so CLI doesn't hang.
		fallback := fmt.Sprintf(`{"type":"control_response","response":{"subtype":"error","request_id":%q,"error":"marshal failure"}}`, requestID)
		data = []byte(fallback)
	}
	if writeErr := s.writeStdin(append(data, '\n')); writeErr != nil {
		s.sendEvent(&ErrorEvent{Err: fmt.Errorf("write control response: %w", writeErr)})
	}
}
