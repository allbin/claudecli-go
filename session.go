package claudecli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

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
	mu sync.Mutex

	// control protocol state
	reqCounter atomic.Int64
	pending    sync.Map // map[string]chan controlResult

	// callbacks
	canUseTool ToolPermissionFunc

	// state tracking
	sessionID  string
	serverInfo json.RawMessage
	stateMu    sync.Mutex
	state     State
	result    *ResultEvent
	err       error
	waited    bool
}

// Events returns the event channel. Closed when session ends.
// Control requests are handled internally and not exposed here.
func (s *Session) Events() <-chan Event { return s.events }

// Query sends a user message to the CLI.
func (s *Session) Query(prompt string) error {
	msg := userMessage{
		Type:            "user",
		SessionID:       s.sessionID,
		Message:         messageBody{Role: "user", Content: prompt},
		ParentToolUseID: nil,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal user message: %w", err)
	}
	return s.writeStdin(append(data, '\n'))
}

// Wait blocks until a ResultEvent or error, draining remaining events.
// Idempotent: multiple calls return the same result.
func (s *Session) Wait() (*ResultEvent, error) {
	s.stateMu.Lock()
	if s.waited {
		result, err := s.result, s.err
		s.stateMu.Unlock()
		return result, err
	}
	s.stateMu.Unlock()

	for range s.events {
	}
	<-s.done

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
	return s.sendControlRequest("set_permission_mode", map[string]string{"mode": string(mode)})
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
	return s.sendControlRequest("rewind_files", map[string]string{"user_message_id": userMessageID})
}

// ReconnectMCPServer reconnects a named MCP server.
func (s *Session) ReconnectMCPServer(serverName string) error {
	return s.sendControlRequest("mcp_reconnect", map[string]string{"server_name": serverName})
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
	return s.sendControlRequest("stop_task", map[string]string{"task_id": taskID})
}

// GetMCPStatus queries MCP server connection status.
func (s *Session) GetMCPStatus() error {
	return s.sendControlRequest("mcp_status", nil)
}

// Close terminates the session.
func (s *Session) Close() error {
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
	if s.proc.Stdin == nil {
		return fmt.Errorf("stdin closed")
	}
	_, err := s.proc.Stdin.Write(data)
	return err
}

// sendControlRequest sends a control request to the CLI (fire-and-forget).
func (s *Session) sendControlRequest(subtype string, data any) error {
	id := fmt.Sprintf("req_%d", s.reqCounter.Add(1))

	reqMap := map[string]any{
		"subtype": subtype,
	}
	if data != nil {
		if m, ok := data.(map[string]string); ok {
			for k, v := range m {
				reqMap[k] = v
			}
		} else if m, ok := data.(map[string]any); ok {
			for k, v := range m {
				reqMap[k] = v
			}
		}
	}

	payload := map[string]any{
		"type":       "control_request",
		"request_id": id,
		"request":    reqMap,
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return s.writeStdin(append(raw, '\n'))
}

// initialize sends the initialize control request and waits for response.
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

	select {
	case result := <-resultCh:
		if result.Err != nil {
			return fmt.Errorf("initialize: %w", result.Err)
		}
		s.serverInfo = result.Response
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

// readLoop reads stdout, routes control messages, forwards events.
func (s *Session) readLoop() {
	defer close(s.done)
	defer close(s.events)

	stderrLines, stderrDone := scanStderr(s.proc, s.events, nil)

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
			s.sendEvent(&ErrorEvent{Err: fmt.Errorf("unmarshal JSONL: %w", err)})
			continue
		}

		switch raw.Type {
		case "control_response":
			s.handleControlResponse(line)

		case "control_request":
			go s.handleControlRequest(raw.RequestID, raw.Request)

		case "system":
			ev := &InitEvent{SessionID: raw.SessionID, Model: raw.Model, Tools: raw.Tools}
			s.sessionID = raw.SessionID
			s.trackState(ev)
			s.sendEvent(ev)

		case "assistant":
			if raw.Message == nil {
				continue
			}
			for _, block := range raw.Message.Content {
				switch block.Type {
				case "thinking":
					s.sendEvent(&ThinkingEvent{Content: block.Thinking, Signature: block.Signature})
				case "text":
					resultText = append(resultText, block.Text)
					s.sendEvent(&TextEvent{Content: block.Text})
				case "tool_use":
					s.sendEvent(&ToolUseEvent{
						ID:    block.ID,
						Name:  block.Name,
						Input: block.Input,
					})
				case "tool_result":
					s.sendEvent(&ToolResultEvent{
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
				Usage: Usage{
					InputTokens:       raw.Usage.InputTokens,
					OutputTokens:      raw.Usage.OutputTokens,
					CacheReadTokens:   raw.Usage.CacheReadInputTokens,
					CacheCreateTokens: raw.Usage.CacheCreationInputTokens,
				},
			}
			resultText = nil
			s.trackState(ev)
			s.sendEvent(ev)

		case "rate_limit_event":
			s.sendEvent(&RateLimitEvent{
				Status:      raw.RateLimitStatus,
				Utilization: raw.RateLimitUtilization,
			})

		case "stream_event":
			s.sendEvent(&StreamEvent{
				UUID:      raw.UUID,
				SessionID: raw.SessionID,
				Event:     raw.Event,
			})
		}
	}

	if err := scanner.Err(); err != nil {
		s.sendEvent(&ErrorEvent{Err: fmt.Errorf("scanner: %w", err)})
	}

	<-stderrDone

	if err := s.proc.Wait(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			ev := &ErrorEvent{Err: err, Fatal: true}
			s.trackState(ev)
			s.sendEvent(ev)
			return
		}
		ev := &ErrorEvent{
			Err: &Error{
				ExitCode: exitErr.ExitCode(),
				Stderr:   strings.Join(*stderrLines, "\n"),
			},
			Fatal: true,
		}
		s.trackState(ev)
		s.sendEvent(ev)
	}
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
		s.state = StateDone
		s.result = e
	case *ErrorEvent:
		if e.Fatal {
			s.state = StateFailed
			if s.err == nil {
				s.err = e.Err
			}
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
	var req rawControlRequestBody
	if err := json.Unmarshal(body, &req); err != nil {
		s.sendControlResponse(requestID, nil, err)
		return
	}

	switch req.Subtype {
	case "can_use_tool":
		if s.canUseTool == nil {
			s.sendControlResponse(requestID, nil, fmt.Errorf("no canUseTool callback registered"))
			return
		}
		var permReq ToolPermissionRequest
		if err := json.Unmarshal(body, &permReq); err != nil {
			s.sendControlResponse(requestID, nil, err)
			return
		}
		resp, err := s.canUseTool(permReq.ToolName, permReq.Input)
		if err != nil {
			s.sendControlResponse(requestID, nil, err)
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

func (s *Session) sendControlResponse(requestID string, response any, err error) {
	var resp rawControlResponse
	resp.Type = "control_response"
	if err != nil {
		resp.Response = controlResponseBody{
			Subtype:   "error",
			RequestID: requestID,
			Error:     err.Error(),
		}
	} else {
		resp.Response = controlResponseBody{
			Subtype:   "success",
			RequestID: requestID,
			Response:  response,
		}
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	s.writeStdin(append(data, '\n'))
}
