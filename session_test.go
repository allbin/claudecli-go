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
		sim.handleInit(t)
		sim.send(`{"type":"system","session_id":"test-sess","model":"sonnet"}`)
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
		sim.handleInit(t)

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
		sim.handleInit(t)

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
		sim.handleInit(t)

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
		sim.handleInit(t)

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
		sim.handleInit(t)
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
		sim.handleInit(t)
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
		sim.handleInit(t)
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
		sim.handleInit(t)

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
		sim.handleInit(t)

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
		sim.handleInit(t)
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
		sim.handleInit(t)

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

func TestSessionQueryRejectRunning(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)
	secondQueryDone := make(chan struct{})

	go func() {
		sim.handleInit(t)
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
		sim.handleInit(t)
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
		sim.handleInit(t)

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
		sim.handleInit(t)
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
		sim.handleInit(t)
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

func TestSessionTextResetOnSystemEvent(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInit(t)

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
