package claudecli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// sessionSim simulates the CLI side of a session for testing.
// Handles the initialize handshake automatically.
type sessionSim struct {
	bidi   *BidiFixtureExecutor
	reader *bufio.Reader
}

func newSessionSim() *sessionSim {
	bidi := NewBidiFixtureExecutor()
	return &sessionSim{
		bidi:   bidi,
		reader: bufio.NewReader(bidi.StdinReader),
	}
}

// handleInit reads and responds to the initialize control request.
func (s *sessionSim) handleInit(t *testing.T) {
	t.Helper()
	s.handleInitWith(t, "{}")
}

// handleInitAndReady handles the init handshake and sends the system event,
// matching real CLI behavior where the system event arrives right after init.
func (s *sessionSim) handleInitAndReady(t *testing.T) {
	t.Helper()
	s.handleInit(t)
	s.send(`{"type":"system","session_id":"test-sess","model":"sonnet"}`)
}

// handleInitWith reads and responds to the initialize control request
// with a custom response body.
func (s *sessionSim) handleInitWith(t *testing.T, responseJSON string) {
	t.Helper()
	line, err := s.reader.ReadBytes('\n')
	if err != nil {
		t.Errorf("read initialize: %v", err)
		return
	}
	var req map[string]any
	json.Unmarshal(line, &req)
	if req["type"] != "control_request" {
		t.Errorf("expected control_request, got %v", req["type"])
	}
	requestID := req["request_id"].(string)
	resp := fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":%s}}`, requestID, responseJSON)
	s.bidi.StdoutWriter.Write([]byte(resp + "\n"))
}

// readStdin reads and parses the next JSON message from stdin.
func (s *sessionSim) readStdin(t *testing.T) map[string]any {
	t.Helper()
	line, _ := s.reader.ReadBytes('\n')
	var msg map[string]any
	json.Unmarshal(line, &msg)
	return msg
}

// respondSuccess reads a control request from stdin and sends a success response.
func (s *sessionSim) respondSuccess(t *testing.T) map[string]any {
	t.Helper()
	msg := s.readStdin(t)
	if msg["type"] != "control_request" {
		t.Errorf("expected control_request, got %v", msg["type"])
	}
	requestID := msg["request_id"].(string)
	resp := fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{}}}`, requestID)
	s.send(resp)
	return msg
}

// respondError reads a control request from stdin and sends an error response.
func (s *sessionSim) respondError(t *testing.T, errMsg string) map[string]any {
	t.Helper()
	msg := s.readStdin(t)
	if msg["type"] != "control_request" {
		t.Errorf("expected control_request, got %v", msg["type"])
	}
	requestID := msg["request_id"].(string)
	resp := fmt.Sprintf(`{"type":"control_response","response":{"subtype":"error","request_id":"%s","error":"%s"}}`, requestID, errMsg)
	s.send(resp)
	return msg
}

// send writes a JSONL line to stdout.
func (s *sessionSim) send(line string) {
	s.bidi.StdoutWriter.Write([]byte(line + "\n"))
}

// sendResult sends system + result events and closes stdout.
func (s *sessionSim) sendResult() {
	s.send(`{"type":"system","session_id":"test-sess","model":"sonnet"}`)
	s.send(`{"type":"result","subtype":"success","session_id":"test-sess","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}`)
	s.bidi.StdoutWriter.Close()
}

// sendTextAndResult sends system + assistant text + result events and closes stdout.
func (s *sessionSim) sendTextAndResult(text string) {
	s.send(`{"type":"system","session_id":"test-sess","model":"sonnet"}`)
	s.send(fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"text","text":"%s"}]}}`, text))
	s.send(`{"type":"result","subtype":"success","session_id":"test-sess","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}`)
	s.bidi.StdoutWriter.Close()
}

func TestSessionInitialize(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)
		sim.send(`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello!"}]}}`)
		sim.send(`{"type":"result","subtype":"success","session_id":"test-sess","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}`)
		sim.bidi.StdoutWriter.Close()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer session.Close()

	var gotInit, gotText, gotResult bool
	for event := range session.Events() {
		switch event.(type) {
		case *InitEvent:
			gotInit = true
		case *TextEvent:
			gotText = true
		case *ResultEvent:
			gotResult = true
		}
	}

	if !gotInit {
		t.Error("missing InitEvent")
	}
	if !gotText {
		t.Error("missing TextEvent")
	}
	if !gotResult {
		t.Error("missing ResultEvent")
	}
}

func TestSessionQuery(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)

		msg := sim.readStdin(t)
		if msg["type"] != "user" {
			t.Errorf("expected user message, got %v", msg["type"])
		}
		body := msg["message"].(map[string]any)
		if body["content"] != "What is Go?" {
			t.Errorf("expected 'What is Go?', got %v", body["content"])
		}

		sim.sendTextAndResult("Response to query")
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	if err := session.Query("What is Go?"); err != nil {
		t.Fatal(err)
	}

	result, err := session.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("nil result")
	}
	if result.Text != "Response to query" {
		t.Errorf("expected 'Response to query', got %q", result.Text)
	}
}

func TestSessionMultiQuery(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)

		// First query
		sim.readStdin(t)
		sim.send(`{"type":"system","session_id":"test-sess","model":"sonnet"}`)
		sim.send(`{"type":"assistant","message":{"content":[{"type":"text","text":"first"}]}}`)
		sim.send(`{"type":"result","subtype":"success","session_id":"test-sess","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}`)

		// Second query
		sim.readStdin(t)
		sim.send(`{"type":"assistant","message":{"content":[{"type":"text","text":"second"}]}}`)
		sim.send(`{"type":"result","subtype":"success","session_id":"test-sess","total_cost_usd":0.02,"usage":{"input_tokens":20,"output_tokens":10}}`)

		sim.bidi.StdoutWriter.Close()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	session.Query("q1")

	// Drain first result
	var results []*ResultEvent
	for event := range session.Events() {
		if r, ok := event.(*ResultEvent); ok {
			results = append(results, r)
			if len(results) == 1 {
				// Send second query after first result
				session.Query("q2")
			}
		}
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	// Text accumulation resets between results
	if results[0].Text != "first" {
		t.Errorf("first result text = %q, want 'first'", results[0].Text)
	}
	if results[1].Text != "second" {
		t.Errorf("second result text = %q, want 'second'", results[1].Text)
	}
}

func TestSessionCanUseTool(t *testing.T) {
	sim := newSessionSim()
	toolCallbackCalled := make(chan bool, 1)
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)

		// Send a can_use_tool control request
		sim.send(`{"type":"control_request","request_id":"cli_req_1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"}}}`)

		// Read the permission response from stdin
		permResp := sim.readStdin(t)
		response := permResp["response"].(map[string]any)
		if response["subtype"] != "success" {
			t.Errorf("expected success, got %v", response["subtype"])
		}
		inner := response["response"].(map[string]any)
		if inner["behavior"] != "allow" {
			t.Errorf("expected allow, got %v", inner["behavior"])
		}

		sim.sendResult()
	}()

	session, err := client.Connect(context.Background(), WithCanUseTool(func(name string, input json.RawMessage) (*PermissionResponse, error) {
		toolCallbackCalled <- true
		if name != "Bash" {
			t.Errorf("expected tool name 'Bash', got %q", name)
		}
		return &PermissionResponse{Allow: true}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	_, err = session.Wait()
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-toolCallbackCalled:
	default:
		t.Error("tool permission callback was not called")
	}
}

func TestSessionCanUseToolDeny(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)

		sim.send(`{"type":"control_request","request_id":"cli_req_1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"rm -rf /"}}}`)

		permResp := sim.readStdin(t)
		response := permResp["response"].(map[string]any)
		inner := response["response"].(map[string]any)
		if inner["behavior"] != "deny" {
			t.Errorf("expected deny, got %v", inner["behavior"])
		}
		if inner["message"] != "dangerous command" {
			t.Errorf("expected 'dangerous command', got %v", inner["message"])
		}

		sim.sendResult()
	}()

	session, err := client.Connect(context.Background(), WithCanUseTool(func(name string, input json.RawMessage) (*PermissionResponse, error) {
		return &PermissionResponse{Allow: false, DenyMessage: "dangerous command"}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	_, err = session.Wait()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSessionClose(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)
		time.Sleep(50 * time.Millisecond)
		sim.bidi.StdoutWriter.Close()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error)
	go func() {
		done <- session.Close()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Close returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close() hung")
	}
}

func TestSessionWaitIdempotent(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)
		sim.sendResult()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	r1, err1 := session.Wait()
	r2, err2 := session.Wait()

	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected errors: %v, %v", err1, err2)
	}
	if r1 != r2 {
		t.Error("Wait() not idempotent")
	}
}

func TestSessionStateTracking(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)
		sim.sendTextAndResult("hi")
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	var gotInit, gotResult bool
	for event := range session.Events() {
		switch event.(type) {
		case *InitEvent:
			gotInit = true
		case *ResultEvent:
			gotResult = true
		}
	}

	if !gotInit {
		t.Error("missing InitEvent")
	}
	if !gotResult {
		t.Error("missing ResultEvent")
	}

	if st := session.State(); st != StateDone {
		t.Errorf("expected StateDone after process exit, got %s", st)
	}
}

func TestSessionInitializeTimeout(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	go func() {
		sim.reader.ReadBytes('\n')
		time.Sleep(100 * time.Millisecond)
		sim.bidi.StdoutWriter.Close()
	}()

	_, err := client.Connect(ctx)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestSessionBuildSessionArgs(t *testing.T) {
	opts := resolveOptions(nil, []Option{
		WithModel(ModelOpus),
		WithSessionID("sess-123"),
	})
	args := opts.buildSessionArgs()

	var hasInputFormat bool
	for i, a := range args {
		if a == "--input-format" && i+1 < len(args) && args[i+1] == "stream-json" {
			hasInputFormat = true
		}
	}
	if !hasInputFormat {
		t.Error("missing --input-format stream-json")
	}

	for _, a := range args {
		if a == "--print" {
			t.Error("session args should not have --print")
		}
		if a == "--no-session-persistence" {
			t.Error("session args should not have --no-session-persistence")
		}
	}
}

func TestSessionBuildSessionArgsWithCanUseTool(t *testing.T) {
	opts := resolveOptions(nil, []Option{
		WithCanUseTool(func(name string, input json.RawMessage) (*PermissionResponse, error) {
			return &PermissionResponse{Allow: true}, nil
		}),
	})
	args := opts.buildSessionArgs()

	var hasPermTool bool
	for i, a := range args {
		if a == "--permission-prompt-tool" && i+1 < len(args) && args[i+1] == "stdio" {
			hasPermTool = true
		}
	}
	if !hasPermTool {
		t.Error("missing --permission-prompt-tool stdio")
	}
}

func TestSessionGetServerInfo(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitWith(t, `{"version":"1.2.3","tools":["Bash","Read"]}`)
		sim.send(`{"type":"system","session_id":"test-sess","model":"sonnet"}`)
		sim.sendResult()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	info := session.GetServerInfo()
	if info == nil {
		t.Fatal("serverInfo is nil")
	}

	var parsed map[string]any
	if err := json.Unmarshal(info, &parsed); err != nil {
		t.Fatalf("unmarshal serverInfo: %v", err)
	}
	if parsed["version"] != "1.2.3" {
		t.Errorf("expected version 1.2.3, got %v", parsed["version"])
	}
}

func TestSessionRewindFiles(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)

		msg := sim.respondSuccess(t)
		request := msg["request"].(map[string]any)
		if request["subtype"] != "rewind_files" {
			t.Errorf("expected rewind_files, got %v", request["subtype"])
		}
		if request["user_message_id"] != "msg-abc-123" {
			t.Errorf("expected msg-abc-123, got %v", request["user_message_id"])
		}

		sim.sendResult()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	if err := session.RewindFiles("msg-abc-123"); err != nil {
		t.Fatal(err)
	}

	_, err = session.Wait()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSessionGetMCPStatus(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)

		msg := sim.respondSuccess(t)
		request := msg["request"].(map[string]any)
		if request["subtype"] != "mcp_status" {
			t.Errorf("expected mcp_status, got %v", request["subtype"])
		}

		sim.sendResult()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	if err := session.GetMCPStatus(); err != nil {
		t.Fatal(err)
	}

	_, err = session.Wait()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSessionStateAndSessionID(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)
		sim.sendTextAndResult("hi")
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	for range session.Events() {
	}

	if id := session.SessionID(); id != "test-sess" {
		t.Errorf("expected session ID 'test-sess', got %q", id)
	}
	if st := session.State(); st != StateDone {
		t.Errorf("expected StateDone after process exit, got %s", st)
	}
}

func TestSessionStateIdleTransition(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)

		sim.readStdin(t)
		sim.send(`{"type":"system","session_id":"test-sess","model":"sonnet"}`)
		sim.send(`{"type":"assistant","message":{"content":[{"type":"text","text":"first"}]}}`)
		sim.send(`{"type":"result","subtype":"success","session_id":"test-sess","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}`)

		sim.readStdin(t)
		sim.send(`{"type":"assistant","message":{"content":[{"type":"text","text":"second"}]}}`)
		sim.send(`{"type":"result","subtype":"success","session_id":"test-sess","total_cost_usd":0.02,"usage":{"input_tokens":20,"output_tokens":10}}`)

		sim.bidi.StdoutWriter.Close()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	session.Query("q1")
	r1, err := session.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if r1.Text != "first" {
		t.Errorf("first result = %q, want 'first'", r1.Text)
	}

	// After first result with readLoop still blocked on scanner.Scan(),
	// state should be Idle (sim is blocked on readStdin, so no race).
	if st := session.State(); st != StateIdle {
		t.Errorf("expected StateIdle after first result, got %s", st)
	}

	// Query succeeds from Idle state
	if err := session.Query("q2"); err != nil {
		t.Fatalf("Query from Idle failed: %v", err)
	}

	r2, err := session.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if r2.Text != "second" {
		t.Errorf("second result = %q, want 'second'", r2.Text)
	}
}

func TestSessionQueryAfterDelay(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)

		sim.readStdin(t)
		sim.send(`{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}`)
		sim.send(`{"type":"result","subtype":"success","session_id":"test-sess","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}`)

		sim.bidi.StdoutWriter.Close()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	// Simulate delay — control requests, app logic, etc.
	time.Sleep(50 * time.Millisecond)

	// This must not fail with "query already in progress"
	if err := session.Query("hello"); err != nil {
		t.Fatalf("Query after delay failed: %v", err)
	}
	r, err := session.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "ok" {
		t.Errorf("result = %q, want 'ok'", r.Text)
	}
}

func TestSessionQueryRejectRunning(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)
	secondQueryDone := make(chan struct{})

	go func() {
		sim.handleInitAndReady(t)
		sim.readStdin(t)
		// Wait until second query attempt is done before responding
		<-secondQueryDone
		sim.sendTextAndResult("done")
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	if err := session.Query("q1"); err != nil {
		t.Fatal(err)
	}

	// Second query while first is running (sim hasn't responded yet)
	err = session.Query("q2")
	close(secondQueryDone)
	if err == nil {
		t.Fatal("expected error for query while running")
	}
	if !strings.Contains(err.Error(), "already in progress") {
		t.Errorf("unexpected error: %v", err)
	}

	session.Wait()
}

func TestSessionQueryRejectFailed(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)
		sim.bidi.StdoutWriter.Close()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Wait for readLoop to finish and set state
	for range session.Events() {
	}
	<-session.done

	err = session.Query("q1")
	if err == nil {
		t.Fatal("expected error for query on ended session")
	}
}

func TestSessionWaitResetAcrossQueries(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)

		sim.readStdin(t)
		sim.send(`{"type":"system","session_id":"test-sess","model":"sonnet"}`)
		sim.send(`{"type":"assistant","message":{"content":[{"type":"text","text":"first"}]}}`)
		sim.send(`{"type":"result","subtype":"success","session_id":"test-sess","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}`)

		sim.readStdin(t)
		sim.send(`{"type":"assistant","message":{"content":[{"type":"text","text":"second"}]}}`)
		sim.send(`{"type":"result","subtype":"success","session_id":"test-sess","total_cost_usd":0.02,"usage":{"input_tokens":20,"output_tokens":10}}`)

		sim.bidi.StdoutWriter.Close()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	session.Query("q1")
	r1, err := session.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if r1.Text != "first" {
		t.Errorf("first Wait() text = %q, want 'first'", r1.Text)
	}

	// Second Wait() after same query returns same result (idempotent)
	r1b, err := session.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if r1b != r1 {
		t.Error("Wait() not idempotent within same query")
	}

	session.Query("q2")
	r2, err := session.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if r2.Text != "second" {
		t.Errorf("second Wait() text = %q, want 'second'", r2.Text)
	}
	if r2 == r1 {
		t.Error("Wait() returned stale result from first query")
	}
}

func TestSessionControlRequestErrorPropagation(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)
		sim.respondError(t, "model not available")
		sim.sendResult()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	err = session.SetModel("invalid-model")
	if err == nil {
		t.Fatal("expected error from rejected control request")
	}
	if !strings.Contains(err.Error(), "model not available") {
		t.Errorf("expected 'model not available' in error, got: %v", err)
	}

	_, err = session.Wait()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSessionControlRequestSuccess(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)
		msg := sim.respondSuccess(t)
		request := msg["request"].(map[string]any)
		if request["subtype"] != "set_model" {
			t.Errorf("expected set_model, got %v", request["subtype"])
		}
		sim.sendResult()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	if err := session.SetModel(ModelSonnet); err != nil {
		t.Fatalf("SetModel failed: %v", err)
	}

	_, err = session.Wait()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSetMaxThinkingTokens(t *testing.T) {
	t.Run("enable", func(t *testing.T) {
		sim := newSessionSim()
		client := NewWithExecutor(sim.bidi)

		go func() {
			sim.handleInitAndReady(t)
			msg := sim.respondSuccess(t)
			request := msg["request"].(map[string]any)
			if request["subtype"] != "set_max_thinking_tokens" {
				t.Errorf("expected set_max_thinking_tokens, got %v", request["subtype"])
			}
			if v, ok := request["max_thinking_tokens"].(float64); !ok || v != 8000 {
				t.Errorf("expected max_thinking_tokens=8000, got %v", request["max_thinking_tokens"])
			}
			sim.sendResult()
		}()

		session, err := client.Connect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()

		if err := session.SetMaxThinkingTokens(8000); err != nil {
			t.Fatalf("SetMaxThinkingTokens failed: %v", err)
		}

		_, err = session.Wait()
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("disable", func(t *testing.T) {
		sim := newSessionSim()
		client := NewWithExecutor(sim.bidi)

		go func() {
			sim.handleInitAndReady(t)
			msg := sim.respondSuccess(t)
			request := msg["request"].(map[string]any)
			if request["subtype"] != "set_max_thinking_tokens" {
				t.Errorf("expected set_max_thinking_tokens, got %v", request["subtype"])
			}
			if v, ok := request["max_thinking_tokens"].(float64); !ok || v != 0 {
				t.Errorf("expected max_thinking_tokens=0, got %v", request["max_thinking_tokens"])
			}
			sim.sendResult()
		}()

		session, err := client.Connect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()

		if err := session.SetMaxThinkingTokens(0); err != nil {
			t.Fatalf("SetMaxThinkingTokens(0) failed: %v", err)
		}

		_, err = session.Wait()
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestSessionTextResetOnSystemEvent(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)

		sim.readStdin(t)
		// System event + text + result for first query
		sim.send(`{"type":"system","session_id":"test-sess","model":"sonnet"}`)
		sim.send(`{"type":"assistant","message":{"content":[{"type":"text","text":"leak"}]}}`)
		sim.send(`{"type":"result","subtype":"success","session_id":"test-sess","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}`)

		// Second query: new system event resets text accumulator
		sim.readStdin(t)
		sim.send(`{"type":"system","session_id":"test-sess","model":"sonnet"}`)
		sim.send(`{"type":"assistant","message":{"content":[{"type":"text","text":"clean"}]}}`)
		sim.send(`{"type":"result","subtype":"success","session_id":"test-sess","total_cost_usd":0.02,"usage":{"input_tokens":20,"output_tokens":10}}`)

		sim.bidi.StdoutWriter.Close()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	session.Query("q1")
	r1, _ := session.Wait()
	if r1.Text != "leak" {
		t.Errorf("first result = %q, want 'leak'", r1.Text)
	}

	session.Query("q2")
	r2, _ := session.Wait()
	if r2.Text != "clean" {
		t.Errorf("second result = %q, want 'clean' (text leaked from previous query)", r2.Text)
	}
}

func TestStateIdleString(t *testing.T) {
	if s := StateIdle.String(); s != "idle" {
		t.Errorf("StateIdle.String() = %q, want 'idle'", s)
	}
}

func TestSessionWaitDoesNotConsumeEvents(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)
		// Send a text event, then a result
		sim.send(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`)
		sim.sendResult()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	// Consume events in one goroutine, call Wait in another.
	// Both should complete without deadlock, and Wait should get the result.
	var events []Event
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for ev := range session.Events() {
			events = append(events, ev)
		}
	}()

	result, err := session.Wait()
	if err != nil {
		t.Fatalf("Wait error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	<-eventsDone
	// The events consumer should have received the text and result events
	if len(events) == 0 {
		t.Error("expected events consumer to receive events")
	}
}

func TestSessionInitTimeoutSeparateFromControlTimeout(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		// Delay the init response by 200ms — longer than the controlTimeout
		// but shorter than the initTimeout.
		line, _ := sim.reader.ReadBytes('\n')
		time.Sleep(200 * time.Millisecond)
		var req map[string]any
		json.Unmarshal(line, &req)
		requestID := req["request_id"].(string)
		resp := fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{}}}`, requestID)
		sim.bidi.StdoutWriter.Write([]byte(resp + "\n"))
		sim.sendResult()
	}()

	// controlTimeout is very short (50ms), but initTimeout is longer (1s).
	// Without separate timeouts, this would fail.
	session, err := client.Connect(context.Background(),
		WithControlTimeout(50*time.Millisecond),
		WithInitTimeout(1*time.Second),
	)
	if err != nil {
		t.Fatalf("Connect should succeed with separate initTimeout: %v", err)
	}
	defer session.Close()

	_, err = session.Wait()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSessionInitTimeoutExpires(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		// Never respond to initialize.
		sim.reader.ReadBytes('\n')
		time.Sleep(500 * time.Millisecond)
		sim.bidi.StdoutWriter.Close()
	}()

	_, err := client.Connect(context.Background(),
		WithInitTimeout(100*time.Millisecond),
	)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("expected timeout error, got: %v", err)
	}
}

func TestSessionPendingRequestsFailOnProcessExit(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)
		// Read the control request from stdin but never respond — just close stdout.
		sim.readStdin(t)
		sim.bidi.StdoutWriter.Close()
	}()

	session, err := client.Connect(context.Background(), WithControlTimeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	errCh := make(chan error, 1)
	go func() {
		errCh <- session.SetModel(ModelSonnet)
	}()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error from pending request on process exit")
		}
		if !strings.Contains(err.Error(), "session ended") {
			t.Errorf("expected 'session ended' error, got: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pending request should fail fast on process exit, not wait for timeout")
	}
}

func TestSessionWriteAfterClose(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)
		sim.bidi.StdoutWriter.Close()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	session.Close()

	err = session.Query("should fail")
	if err == nil {
		t.Fatal("expected error writing to closed session")
	}
	// May hit state check ("session ended") or writeStdin guard ("session closed")
	if !strings.Contains(err.Error(), "session") {
		t.Errorf("expected session-related error, got: %v", err)
	}
}

func TestSessionCanUseToolCallbackPanic(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)

		// Send a can_use_tool request — callback will panic
		sim.send(`{"type":"control_request","request_id":"cli_req_1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"}}}`)

		// Read the error response (should be error from panic recovery)
		permResp := sim.readStdin(t)
		response := permResp["response"].(map[string]any)
		if response["subtype"] != "error" {
			t.Errorf("expected error response from panicking callback, got %v", response["subtype"])
		}

		sim.sendResult()
	}()

	session, err := client.Connect(context.Background(), WithCanUseTool(func(name string, input json.RawMessage) (*PermissionResponse, error) {
		panic("intentional test panic")
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	_, err = session.Wait()
	if err != nil {
		t.Fatal(err)
	}
}

// Fix #4: sendControlResponse error path sends error response to CLI.
func TestSessionControlResponseErrorPath(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)

		// Send a can_use_tool request; callback returns an error
		sim.send(`{"type":"control_request","request_id":"cli_req_1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"}}}`)

		// Read response — should be an error response
		permResp := sim.readStdin(t)
		response := permResp["response"].(map[string]any)
		if response["subtype"] != "error" {
			t.Errorf("expected error response from callback error, got %v", response["subtype"])
		}
		if response["request_id"] != "cli_req_1" {
			t.Errorf("expected request_id 'cli_req_1', got %v", response["request_id"])
		}

		// Send result so session ends cleanly
		sim.sendResult()
	}()

	session, err := client.Connect(context.Background(), WithCanUseTool(func(name string, input json.RawMessage) (*PermissionResponse, error) {
		return nil, fmt.Errorf("callback failed")
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	_, err = session.Wait()
	if err != nil {
		t.Fatal(err)
	}
}

// Fix #9: subtype set after maps.Copy — data with a "subtype" key shouldn't corrupt the request.
func TestSessionControlRequestSubtypeNotCorrupted(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)

		// Read control request and verify subtype is correct despite data containing "subtype"
		msg := sim.readStdin(t)
		request := msg["request"].(map[string]any)
		if request["subtype"] != "rewind_files" {
			t.Errorf("expected subtype 'rewind_files', got %v (corrupted by data)", request["subtype"])
		}

		requestID := msg["request_id"].(string)
		resp := fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{}}}`, requestID)
		sim.send(resp)

		sim.sendResult()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	// RewindFiles sends data with user_message_id. We test that sendControlRequest
	// correctly sets subtype after maps.Copy so data can't override it.
	if err := session.RewindFiles("msg-123"); err != nil {
		t.Fatal(err)
	}

	_, err = session.Wait()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSessionCanUseToolCancelledDuringCallback(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	callbackStarted := make(chan struct{})

	go func() {
		sim.handleInitAndReady(t)

		// Send a can_use_tool request — callback will block
		sim.send(`{"type":"control_request","request_id":"cli_req_1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"}}}`)

		// Wait for callback to start, then close stdout to end session
		<-callbackStarted
		sim.bidi.StdoutWriter.Close()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	session, err := client.Connect(ctx, WithCanUseTool(func(name string, input json.RawMessage) (*PermissionResponse, error) {
		close(callbackStarted)
		// Block until context is cancelled
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	if err != nil {
		t.Fatal(err)
	}

	// Cancel context while callback is blocked
	done := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
		session.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close() hung — callback not cancelled by context")
	}
}

func TestSessionUserInputRouting(t *testing.T) {
	sim := newSessionSim()
	callbackCalled := make(chan bool, 1)
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)

		// Send AskUserQuestion via can_use_tool
		sim.send(`{"type":"control_request","request_id":"cli_req_1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Which approach?","header":"Strategy","options":[{"label":"A","description":"Fast"},{"label":"B","description":"Safe"}],"multiSelect":false}]}}}`)

		permResp := sim.readStdin(t)
		response := permResp["response"].(map[string]any)
		if response["subtype"] != "success" {
			t.Errorf("expected success, got %v", response["subtype"])
		}
		inner := response["response"].(map[string]any)
		if inner["behavior"] != "allow" {
			t.Errorf("expected allow, got %v", inner["behavior"])
		}
		updatedInput := inner["updatedInput"].(map[string]any)
		answers := updatedInput["answers"].(map[string]any)
		if answers["Which approach?"] != "A" {
			t.Errorf("expected answer 'A', got %v", answers["Which approach?"])
		}
		questions := updatedInput["questions"].([]any)
		if len(questions) != 1 {
			t.Errorf("expected 1 question in updatedInput, got %d", len(questions))
		}

		sim.sendResult()
	}()

	session, err := client.Connect(context.Background(), WithUserInput(func(questions []Question) (map[string]string, error) {
		callbackCalled <- true
		if len(questions) != 1 {
			t.Errorf("expected 1 question, got %d", len(questions))
		}
		if questions[0].Question != "Which approach?" {
			t.Errorf("expected 'Which approach?', got %q", questions[0].Question)
		}
		if questions[0].Header != "Strategy" {
			t.Errorf("expected header 'Strategy', got %q", questions[0].Header)
		}
		if len(questions[0].Options) != 2 {
			t.Errorf("expected 2 options, got %d", len(questions[0].Options))
		}
		return map[string]string{"Which approach?": "A"}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	_, err = session.Wait()
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-callbackCalled:
	default:
		t.Error("userInput callback was not called")
	}
}

func TestSessionUserInputFallsThroughToCanUseTool(t *testing.T) {
	sim := newSessionSim()
	canUseToolCalled := make(chan bool, 1)
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)

		// Send AskUserQuestion — no userInput registered, should go to canUseTool
		sim.send(`{"type":"control_request","request_id":"cli_req_1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Pick one"}]}}}`)

		permResp := sim.readStdin(t)
		response := permResp["response"].(map[string]any)
		if response["subtype"] != "success" {
			t.Errorf("expected success, got %v", response["subtype"])
		}

		sim.sendResult()
	}()

	session, err := client.Connect(context.Background(), WithCanUseTool(func(name string, input json.RawMessage) (*PermissionResponse, error) {
		canUseToolCalled <- true
		if name != "AskUserQuestion" {
			t.Errorf("expected AskUserQuestion, got %q", name)
		}
		return &PermissionResponse{Allow: true}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	_, err = session.Wait()
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-canUseToolCalled:
	default:
		t.Error("canUseTool callback was not called for AskUserQuestion without userInput")
	}
}

func TestSessionUserInputDoesNotInterceptOtherTools(t *testing.T) {
	sim := newSessionSim()
	canUseToolCalled := make(chan bool, 1)
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)

		// Send a Bash tool request — should go to canUseTool, not userInput
		sim.send(`{"type":"control_request","request_id":"cli_req_1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"}}}`)

		permResp := sim.readStdin(t)
		response := permResp["response"].(map[string]any)
		if response["subtype"] != "success" {
			t.Errorf("expected success, got %v", response["subtype"])
		}

		sim.sendResult()
	}()

	session, err := client.Connect(context.Background(),
		WithUserInput(func(questions []Question) (map[string]string, error) {
			t.Error("userInput should not be called for Bash tool")
			return nil, nil
		}),
		WithCanUseTool(func(name string, input json.RawMessage) (*PermissionResponse, error) {
			canUseToolCalled <- true
			return &PermissionResponse{Allow: true}, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	_, err = session.Wait()
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-canUseToolCalled:
	default:
		t.Error("canUseTool was not called for Bash tool")
	}
}

func TestSessionUserInputOnlyNoCanUseTool(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)

		// Send a Bash tool request — no canUseTool registered, should get error
		sim.send(`{"type":"control_request","request_id":"cli_req_1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"}}}`)

		permResp := sim.readStdin(t)
		response := permResp["response"].(map[string]any)
		if response["subtype"] != "error" {
			t.Errorf("expected error for non-AskUserQuestion without canUseTool, got %v", response["subtype"])
		}

		sim.sendResult()
	}()

	session, err := client.Connect(context.Background(), WithUserInput(func(questions []Question) (map[string]string, error) {
		return map[string]string{}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	_, err = session.Wait()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSessionUserInputCallbackPanic(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)

		sim.send(`{"type":"control_request","request_id":"cli_req_1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Pick one"}]}}}`)

		permResp := sim.readStdin(t)
		response := permResp["response"].(map[string]any)
		if response["subtype"] != "error" {
			t.Errorf("expected error from panicking callback, got %v", response["subtype"])
		}

		sim.sendResult()
	}()

	session, err := client.Connect(context.Background(), WithUserInput(func(questions []Question) (map[string]string, error) {
		panic("intentional test panic")
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	_, err = session.Wait()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSessionUserInputCallbackError(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)

		sim.send(`{"type":"control_request","request_id":"cli_req_1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Pick one"}]}}}`)

		permResp := sim.readStdin(t)
		response := permResp["response"].(map[string]any)
		if response["subtype"] != "error" {
			t.Errorf("expected error from failing callback, got %v", response["subtype"])
		}

		sim.sendResult()
	}()

	session, err := client.Connect(context.Background(), WithUserInput(func(questions []Question) (map[string]string, error) {
		return nil, fmt.Errorf("user cancelled")
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	_, err = session.Wait()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSessionUserInputCancelledDuringCallback(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	callbackStarted := make(chan struct{})

	go func() {
		sim.handleInitAndReady(t)

		sim.send(`{"type":"control_request","request_id":"cli_req_1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Pick one"}]}}}`)

		<-callbackStarted
		sim.bidi.StdoutWriter.Close()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	session, err := client.Connect(ctx, WithUserInput(func(questions []Question) (map[string]string, error) {
		close(callbackStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
		session.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close() hung — userInput callback not cancelled by context")
	}
}

func TestBuildSessionArgsWithUserInputOnly(t *testing.T) {
	opts := resolveOptions(nil, []Option{
		WithUserInput(func(questions []Question) (map[string]string, error) {
			return nil, nil
		}),
	})
	args := opts.buildSessionArgs()

	var hasPermTool bool
	for i, a := range args {
		if a == "--permission-prompt-tool" && i+1 < len(args) && args[i+1] == "stdio" {
			hasPermTool = true
		}
	}
	if !hasPermTool {
		t.Error("WithUserInput alone should add --permission-prompt-tool stdio")
	}
}

func TestPrepareQueryEdgeCases(t *testing.T) {
	t.Run("running rejects", func(t *testing.T) {
		sim := newSessionSim()
		client := NewWithExecutor(sim.bidi)

		checked := make(chan struct{})
		go func() {
			sim.handleInitAndReady(t)
			sim.readStdin(t) // consume the query
			// Keep stdout open until assertion completes
			<-checked
			sim.bidi.StdoutWriter.Close()
		}()

		session, err := client.Connect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()

		if err := session.Query("first"); err != nil {
			t.Fatal(err)
		}

		err = session.prepareQuery()
		close(checked)
		if err == nil || !strings.Contains(err.Error(), "query already in progress") {
			t.Fatalf("expected 'query already in progress', got %v", err)
		}
	})

	t.Run("done rejects", func(t *testing.T) {
		sim := newSessionSim()
		client := NewWithExecutor(sim.bidi)

		go func() {
			sim.handleInitAndReady(t)
			sim.sendResult()
		}()

		session, err := client.Connect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()

		// Wait for result, then drain events so process exits cleanly
		session.Wait()
		for range session.Events() {
		}

		// readLoop has exited and setDoneState ran — state is now Done
		err = session.prepareQuery()
		if err == nil || !strings.Contains(err.Error(), "session ended") {
			t.Fatalf("expected 'session ended', got %v", err)
		}
	})

	t.Run("idle succeeds", func(t *testing.T) {
		sim := newSessionSim()
		client := NewWithExecutor(sim.bidi)

		go func() {
			sim.handleInitAndReady(t)
			// Send result to produce a ResultEvent (state stays Idle)
			sim.send(`{"type":"result","subtype":"success","session_id":"test-sess","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}`)
			// Read the query that prepareQuery + sendUserMessage will produce
			sim.readStdin(t)
			sim.bidi.StdoutWriter.Close()
		}()

		session, err := client.Connect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()

		// Wait for result so state becomes Idle
		session.Wait()

		err = session.prepareQuery()
		if err != nil {
			t.Fatalf("expected nil error from idle state, got %v", err)
		}
		if session.State() != StateRunning {
			t.Fatalf("expected StateRunning after prepareQuery, got %s", session.State())
		}

		// Send a message so the sim goroutine can read and close
		session.sendUserMessage("cleanup")
	})
}
