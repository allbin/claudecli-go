package claudecli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestSessionInitialize(t *testing.T) {
	bidi := NewBidiFixtureExecutor()
	client := NewWithExecutor(bidi)

	go func() {
		reader := bufio.NewReader(bidi.StdinReader)

		// Read the initialize request
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Errorf("read initialize: %v", err)
			return
		}
		var req map[string]any
		json.Unmarshal(line, &req)

		if req["type"] != "control_request" {
			t.Errorf("expected control_request, got %v", req["type"])
		}

		// Send back success response
		requestID := req["request_id"].(string)
		resp := fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{}}}`, requestID)
		bidi.StdoutWriter.Write([]byte(resp + "\n"))

		// Send system event
		bidi.StdoutWriter.Write([]byte(`{"type":"system","session_id":"test-sess","model":"sonnet"}` + "\n"))

		// Send assistant response
		bidi.StdoutWriter.Write([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello!"}]}}` + "\n"))

		// Send result
		bidi.StdoutWriter.Write([]byte(`{"type":"result","subtype":"success","session_id":"test-sess","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}` + "\n"))

		bidi.StdoutWriter.Close()
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
	bidi := NewBidiFixtureExecutor()
	client := NewWithExecutor(bidi)

	go func() {
		reader := bufio.NewReader(bidi.StdinReader)

		// Handle initialize
		line, _ := reader.ReadBytes('\n')
		var req map[string]any
		json.Unmarshal(line, &req)
		requestID := req["request_id"].(string)
		resp := fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{}}}`, requestID)
		bidi.StdoutWriter.Write([]byte(resp + "\n"))

		// Wait for the user query
		queryLine, _ := reader.ReadBytes('\n')
		var queryMsg map[string]any
		json.Unmarshal(queryLine, &queryMsg)

		if queryMsg["type"] != "user" {
			t.Errorf("expected user message, got %v", queryMsg["type"])
		}

		msg := queryMsg["message"].(map[string]any)
		if msg["content"] != "What is Go?" {
			t.Errorf("expected 'What is Go?', got %v", msg["content"])
		}

		// Send response
		bidi.StdoutWriter.Write([]byte(`{"type":"system","session_id":"test-sess","model":"sonnet"}` + "\n"))
		bidi.StdoutWriter.Write([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"Response to query"}]}}` + "\n"))
		bidi.StdoutWriter.Write([]byte(`{"type":"result","subtype":"success","session_id":"test-sess","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}` + "\n"))
		bidi.StdoutWriter.Close()
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

func TestSessionCanUseTool(t *testing.T) {
	bidi := NewBidiFixtureExecutor()

	toolCallbackCalled := make(chan bool, 1)

	client := NewWithExecutor(bidi)

	go func() {
		reader := bufio.NewReader(bidi.StdinReader)

		// Handle initialize
		line, _ := reader.ReadBytes('\n')
		var req map[string]any
		json.Unmarshal(line, &req)
		requestID := req["request_id"].(string)
		resp := fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{}}}`, requestID)
		bidi.StdoutWriter.Write([]byte(resp + "\n"))

		// Send a can_use_tool control request
		bidi.StdoutWriter.Write([]byte(`{"type":"control_request","request_id":"cli_req_1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"}}}` + "\n"))

		// Read the permission response from stdin
		permResp, _ := reader.ReadBytes('\n')
		var permRespMsg map[string]any
		json.Unmarshal(permResp, &permRespMsg)

		// Verify it's a success response
		response := permRespMsg["response"].(map[string]any)
		if response["subtype"] != "success" {
			t.Errorf("expected success, got %v", response["subtype"])
		}

		// Verify the behavior is allow
		inner := response["response"].(map[string]any)
		if inner["behavior"] != "allow" {
			t.Errorf("expected allow, got %v", inner["behavior"])
		}

		// Send result to end session
		bidi.StdoutWriter.Write([]byte(`{"type":"system","session_id":"test-sess","model":"sonnet"}` + "\n"))
		bidi.StdoutWriter.Write([]byte(`{"type":"result","subtype":"success","session_id":"test-sess","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}` + "\n"))
		bidi.StdoutWriter.Close()
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
	bidi := NewBidiFixtureExecutor()
	client := NewWithExecutor(bidi)

	go func() {
		reader := bufio.NewReader(bidi.StdinReader)

		// Handle initialize
		line, _ := reader.ReadBytes('\n')
		var req map[string]any
		json.Unmarshal(line, &req)
		requestID := req["request_id"].(string)
		resp := fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{}}}`, requestID)
		bidi.StdoutWriter.Write([]byte(resp + "\n"))

		// Send a can_use_tool control request
		bidi.StdoutWriter.Write([]byte(`{"type":"control_request","request_id":"cli_req_1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"rm -rf /"}}}` + "\n"))

		// Read the permission response
		permResp, _ := reader.ReadBytes('\n')
		var permRespMsg map[string]any
		json.Unmarshal(permResp, &permRespMsg)

		response := permRespMsg["response"].(map[string]any)
		inner := response["response"].(map[string]any)
		if inner["behavior"] != "deny" {
			t.Errorf("expected deny, got %v", inner["behavior"])
		}
		if inner["message"] != "dangerous command" {
			t.Errorf("expected 'dangerous command', got %v", inner["message"])
		}

		// Send result
		bidi.StdoutWriter.Write([]byte(`{"type":"system","session_id":"test-sess","model":"sonnet"}` + "\n"))
		bidi.StdoutWriter.Write([]byte(`{"type":"result","subtype":"success","session_id":"test-sess","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}` + "\n"))
		bidi.StdoutWriter.Close()
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
	bidi := NewBidiFixtureExecutor()
	client := NewWithExecutor(bidi)

	go func() {
		reader := bufio.NewReader(bidi.StdinReader)

		// Handle initialize
		line, _ := reader.ReadBytes('\n')
		var req map[string]any
		json.Unmarshal(line, &req)
		requestID := req["request_id"].(string)
		resp := fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{}}}`, requestID)
		bidi.StdoutWriter.Write([]byte(resp + "\n"))

		// Keep stdout open until the test closes it
		// (simulate a long-running session)
		// The Close() call will cancel the context
		time.Sleep(50 * time.Millisecond)
		bidi.StdoutWriter.Close()
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
	bidi := NewBidiFixtureExecutor()
	client := NewWithExecutor(bidi)

	go func() {
		reader := bufio.NewReader(bidi.StdinReader)

		// Handle initialize
		line, _ := reader.ReadBytes('\n')
		var req map[string]any
		json.Unmarshal(line, &req)
		requestID := req["request_id"].(string)
		resp := fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{}}}`, requestID)
		bidi.StdoutWriter.Write([]byte(resp + "\n"))

		bidi.StdoutWriter.Write([]byte(`{"type":"system","session_id":"test-sess","model":"sonnet"}` + "\n"))
		bidi.StdoutWriter.Write([]byte(`{"type":"result","subtype":"success","session_id":"test-sess","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}` + "\n"))
		bidi.StdoutWriter.Close()
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
	bidi := NewBidiFixtureExecutor()
	client := NewWithExecutor(bidi)

	go func() {
		reader := bufio.NewReader(bidi.StdinReader)

		line, _ := reader.ReadBytes('\n')
		var req map[string]any
		json.Unmarshal(line, &req)
		requestID := req["request_id"].(string)
		resp := fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{}}}`, requestID)
		bidi.StdoutWriter.Write([]byte(resp + "\n"))

		bidi.StdoutWriter.Write([]byte(`{"type":"system","session_id":"test-sess","model":"sonnet"}` + "\n"))
		bidi.StdoutWriter.Write([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}` + "\n"))
		bidi.StdoutWriter.Write([]byte(`{"type":"result","subtype":"success","session_id":"test-sess","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}` + "\n"))
		bidi.StdoutWriter.Close()
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

	// After all events consumed, final state should be StateDone
	session.stateMu.Lock()
	st := session.state
	session.stateMu.Unlock()
	if st != StateDone {
		t.Errorf("expected StateDone after all events, got %s", st)
	}
}

func TestSessionInitializeTimeout(t *testing.T) {
	bidi := NewBidiFixtureExecutor()
	client := NewWithExecutor(bidi)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Don't respond to initialize — should timeout
	go func() {
		// Read the initialize request but don't respond
		reader := bufio.NewReader(bidi.StdinReader)
		reader.ReadBytes('\n')
		// Let it timeout, then close
		time.Sleep(100 * time.Millisecond)
		bidi.StdoutWriter.Close()
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

	// Should have --input-format stream-json
	var hasInputFormat bool
	for i, a := range args {
		if a == "--input-format" && i+1 < len(args) && args[i+1] == "stream-json" {
			hasInputFormat = true
		}
	}
	if !hasInputFormat {
		t.Error("missing --input-format stream-json")
	}

	// Should NOT have --print (sessions don't use --print)
	for _, a := range args {
		if a == "--print" {
			t.Error("session args should not have --print")
		}
	}

	// Should NOT have --no-session-persistence
	for _, a := range args {
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
	bidi := NewBidiFixtureExecutor()
	client := NewWithExecutor(bidi)

	go func() {
		reader := bufio.NewReader(bidi.StdinReader)

		line, _ := reader.ReadBytes('\n')
		var req map[string]any
		json.Unmarshal(line, &req)
		requestID := req["request_id"].(string)

		resp := fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{"version":"1.2.3","tools":["Bash","Read"]}}}`, requestID)
		bidi.StdoutWriter.Write([]byte(resp + "\n"))

		bidi.StdoutWriter.Write([]byte(`{"type":"system","session_id":"test-sess","model":"sonnet"}` + "\n"))
		bidi.StdoutWriter.Write([]byte(`{"type":"result","subtype":"success","session_id":"test-sess","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}` + "\n"))
		bidi.StdoutWriter.Close()
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
	bidi := NewBidiFixtureExecutor()
	client := NewWithExecutor(bidi)

	go func() {
		reader := bufio.NewReader(bidi.StdinReader)

		line, _ := reader.ReadBytes('\n')
		var req map[string]any
		json.Unmarshal(line, &req)
		requestID := req["request_id"].(string)
		resp := fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{}}}`, requestID)
		bidi.StdoutWriter.Write([]byte(resp + "\n"))

		ctrlLine, _ := reader.ReadBytes('\n')
		var ctrlReq map[string]any
		json.Unmarshal(ctrlLine, &ctrlReq)

		if ctrlReq["type"] != "control_request" {
			t.Errorf("expected control_request, got %v", ctrlReq["type"])
		}
		request := ctrlReq["request"].(map[string]any)
		if request["subtype"] != "rewind_files" {
			t.Errorf("expected rewind_files, got %v", request["subtype"])
		}
		if request["user_message_id"] != "msg-abc-123" {
			t.Errorf("expected msg-abc-123, got %v", request["user_message_id"])
		}

		bidi.StdoutWriter.Write([]byte(`{"type":"system","session_id":"test-sess","model":"sonnet"}` + "\n"))
		bidi.StdoutWriter.Write([]byte(`{"type":"result","subtype":"success","session_id":"test-sess","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}` + "\n"))
		bidi.StdoutWriter.Close()
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
	bidi := NewBidiFixtureExecutor()
	client := NewWithExecutor(bidi)

	go func() {
		reader := bufio.NewReader(bidi.StdinReader)

		line, _ := reader.ReadBytes('\n')
		var req map[string]any
		json.Unmarshal(line, &req)
		requestID := req["request_id"].(string)
		resp := fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{}}}`, requestID)
		bidi.StdoutWriter.Write([]byte(resp + "\n"))

		ctrlLine, _ := reader.ReadBytes('\n')
		var ctrlReq map[string]any
		json.Unmarshal(ctrlLine, &ctrlReq)

		if ctrlReq["type"] != "control_request" {
			t.Errorf("expected control_request, got %v", ctrlReq["type"])
		}
		request := ctrlReq["request"].(map[string]any)
		if request["subtype"] != "mcp_status" {
			t.Errorf("expected mcp_status, got %v", request["subtype"])
		}

		bidi.StdoutWriter.Write([]byte(`{"type":"system","session_id":"test-sess","model":"sonnet"}` + "\n"))
		bidi.StdoutWriter.Write([]byte(`{"type":"result","subtype":"success","session_id":"test-sess","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}` + "\n"))
		bidi.StdoutWriter.Close()
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
