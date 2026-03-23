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
	}
	// Valid: StateStarting (initial query), StateIdle (subsequent queries)
	s.state = StateRunning
	s.waited = false
	s.result = nil
	s.err = nil
	s.resultReady = make(chan struct{})
	s.resultCloseOnce = sync.Once{}
	return nil
}

// sendUserMessage marshals and writes a user message with the given content.
func (s *Session) sendUserMessage(content any) error {
	msg := userMessage{
		Type:            "user",
		SessionID:       s.sessionID,
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
	return s.sendUserMessage(content)
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
	return s.sendControlRequest("mcp_reconnect", map[string]any{"server_name": serverName})
}

// ToggleMCPServer enables or disables a named MCP server.
func (s *Session) ToggleMCPServer(serverName string, enabled bool) error {
	return s.sendControlRequest("mcp_toggle", map[string]any{
		"server_name": serverName,
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

// Close terminates the session. Closes stdin first (EOF signal to CLI),
// then cancels the context (SIGTERM) as backup.
func (s *Session) Close() error {
	s.mu.Lock()
	s.stdinClosed = true
	if s.proc.Stdin != nil {
		s.proc.Stdin.Close()
	}
	s.mu.Unlock()

	s.cancel()
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
		return err
	}
	if err := s.writeStdin(append(raw, '\n')); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(s.ctx, s.controlTimeout)
	defer cancel()

	select {
	case result := <-resultCh:
		if result.Err != nil {
			return fmt.Errorf("%s: %w", subtype, result.Err)
		}
		return nil
	case <-ctx.Done():
		if s.ctx.Err() != nil {
			return s.ctx.Err()
		}
		return fmt.Errorf("%s: timeout after %s", subtype, s.controlTimeout)
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
	// on a slow event consumer.
	pump := make(chan Event, 256)
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
	defer func() {
		close(pump)
		<-pumpDone
		close(s.events)
	}()

	pumpSend := func(ev Event) {
		select {
		case pump <- ev:
		case <-s.ctx.Done():
		}
	}

	stderrLines, stderrDone := scanStderr(s.ctx, s.proc, pump, nil)

	scanner := bufio.NewScanner(s.proc.Stdout)
	scanner.Buffer(make([]byte, 256*1024), 10*1024*1024)

	var resultText []string

	for scanner.Scan() {
		line := scanner.Bytes()
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
			resultText = nil
			ev := &InitEvent{SessionID: raw.SessionID, Model: raw.Model, Tools: raw.Tools}
			s.stateMu.Lock()
			s.sessionID = raw.SessionID
			s.stateMu.Unlock()
			s.trackState(ev)
			pumpSend(ev)

		case "assistant":
			if raw.Message == nil {
				continue
			}
			for _, block := range raw.Message.Content {
				switch block.Type {
				case "thinking":
					pumpSend(&ThinkingEvent{Content: block.Thinking, Signature: block.Signature})
				case "text":
					resultText = append(resultText, block.Text)
					pumpSend(&TextEvent{Content: block.Text})
				case "tool_use":
					pumpSend(&ToolUseEvent{
						ID:    block.ID,
						Name:  block.Name,
						Input: block.Input,
					})
				case "tool_result":
					pumpSend(&ToolResultEvent{
						ToolUseID: block.ToolUseID,
						Content:   extractContent(block.Content),
					})
				}
			}

		case "result":
			ev := &ResultEvent{
				Text:             strings.Join(resultText, ""),
				Subtype:          raw.Subtype,
				StopReason:       raw.StopReason,
				StructuredOutput: raw.StructuredOutput,
				Duration:         time.Duration(raw.DurationMS) * time.Millisecond,
				CostUSD:          raw.CostUSD,
				SessionID:        raw.SessionID,
				Usage:            raw.Usage.toUsage(),
			}
			resultText = nil
			s.trackState(ev)
			pumpSend(ev)

		case "rate_limit_event":
			pumpSend(&RateLimitEvent{
				Status:      raw.RateLimitInfo.Status,
				Utilization: raw.RateLimitInfo.Utilization,
			})

		case "stream_event":
			pumpSend(&StreamEvent{
				UUID:      raw.UUID,
				SessionID: raw.SessionID,
				Event:     raw.Event,
			})
		}
	}

	if err := scanner.Err(); err != nil {
		pumpSend(&ErrorEvent{Err: fmt.Errorf("scanner: %w", err)})
	}

	// Wait for in-flight handleControlRequest goroutines before closing pump.
	s.controlWg.Wait()

	<-stderrDone

	if err := s.proc.Wait(); err != nil {
		stderr := strings.Join(*stderrLines, "\n")
		ev := &ErrorEvent{
			Err:   processExitError(err, stderr),
			Fatal: true,
		}
		s.trackState(ev)
		pumpSend(ev)
	}
}

// setDoneState transitions to StateDone when readLoop exits (process ended).
func (s *Session) setDoneState() {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.state != StateFailed {
		s.state = StateDone
	}
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

func (s *Session) sendEvent(ev Event) {
	select {
	case s.events <- ev:
	case <-s.ctx.Done():
	}
}

func (s *Session) trackState(event Event) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	switch e := event.(type) {
	case *InitEvent:
		s.state = StateRunning
	case *ResultEvent:
		s.state = StateIdle
		s.result = e
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
