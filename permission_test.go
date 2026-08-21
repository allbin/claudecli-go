package claudecli

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
)

// Verbatim from a live can_use_tool control request (Claude CLI 2.1.235):
// a Write outside the allowed working directory, in default permission mode.
const canUseToolRequest = `{"type":"control_request","request_id":"cli-1","request":{"subtype":"can_use_tool","tool_name":"Write","display_name":"Write","input":{"file_path":"/tmp/probe-target.txt","content":"hello\n"},"description":"/tmp/probe-target.txt","permission_suggestions":[{"type":"setMode","mode":"acceptEdits","destination":"session"},{"type":"addDirectories","directories":["/tmp"],"destination":"session"}],"decision_reason":"Path is outside allowed working directories","decision_reason_type":"workingDir","tool_use_id":"toolu_01NbjBgwHU6dbKU6ZXYiuwxS","agent_id":"agent_3"}}`

// The whole point of the request-shaped callback: a host can attribute the
// prompt and explain it, none of which the name+input signature carries.
func TestCanUseToolRequestCarriesFullRequest(t *testing.T) {
	sim := newSessionSim()

	got := make(chan ToolPermissionRequest, 1)
	client := NewWithExecutor(sim.bidi, WithCanUseToolRequest(
		func(req ToolPermissionRequest) (*PermissionResponse, error) {
			got <- req
			return &PermissionResponse{Allow: true}, nil
		}))

	go func() {
		sim.handleInitAndReady(t)
		sim.send(canUseToolRequest)
		sim.readStdin(t) // the control_response
		sim.sendResult()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	req := <-got
	if req.ToolUseID != "toolu_01NbjBgwHU6dbKU6ZXYiuwxS" {
		t.Errorf("ToolUseID = %q", req.ToolUseID)
	}
	if req.AgentID != "agent_3" {
		t.Errorf("AgentID = %q", req.AgentID)
	}
	if req.DecisionReasonType != "workingDir" {
		t.Errorf("DecisionReasonType = %q", req.DecisionReasonType)
	}
	if req.DecisionReason != "Path is outside allowed working directories" {
		t.Errorf("DecisionReason = %q", req.DecisionReason)
	}
	if req.DisplayName != "Write" || req.Description != "/tmp/probe-target.txt" {
		t.Errorf("presentation fields = %q / %q", req.DisplayName, req.Description)
	}
	if len(req.PermissionSuggestions) != 2 {
		t.Fatalf("PermissionSuggestions = %d, want 2", len(req.PermissionSuggestions))
	}
	var first struct {
		Type        string `json:"type"`
		Destination string `json:"destination"`
	}
	if err := json.Unmarshal(req.PermissionSuggestions[0], &first); err != nil {
		t.Fatalf("suggestion not decodable: %v", err)
	}
	if first.Type != "setMode" || first.Destination != "session" {
		t.Errorf("suggestion[0] = %+v", first)
	}

	if _, err := session.Wait(); err != nil {
		t.Fatal(err)
	}
}

// The "always allow" flow: accepted suggestions must reach the CLI verbatim.
func TestPermissionResponseCarriesUpdatedPermissions(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi, WithCanUseToolRequest(
		func(req ToolPermissionRequest) (*PermissionResponse, error) {
			return &PermissionResponse{
				Allow:              true,
				UpdatedPermissions: req.PermissionSuggestions,
			}, nil
		}))

	responded := make(chan map[string]any, 1)
	go func() {
		sim.handleInitAndReady(t)
		sim.send(canUseToolRequest)
		responded <- sim.readStdin(t)
		sim.sendResult()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	msg := <-responded
	resp := msg["response"].(map[string]any)
	body, ok := resp["response"].(map[string]any)
	if !ok {
		t.Fatalf("response body missing: %#v", resp)
	}
	if body["behavior"] != "allow" {
		t.Errorf("behavior = %v", body["behavior"])
	}
	updated, ok := body["updatedPermissions"].([]any)
	if !ok {
		t.Fatalf("updatedPermissions missing: %#v", body)
	}
	if len(updated) != 2 {
		t.Errorf("updatedPermissions = %d, want 2", len(updated))
	}

	if _, err := session.Wait(); err != nil {
		t.Fatal(err)
	}
}

// Deny-and-stop must be distinguishable from deny-with-guidance, and an
// ordinary denial must not carry the field at all.
func TestPermissionDenyInterrupt(t *testing.T) {
	for _, tc := range []struct {
		name      string
		interrupt bool
		want      bool
	}{
		{"interrupt set", true, true},
		{"interrupt unset", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sim := newSessionSim()
			client := NewWithExecutor(sim.bidi, WithCanUseTool(
				func(string, json.RawMessage) (*PermissionResponse, error) {
					return &PermissionResponse{
						Allow:       false,
						DenyMessage: "no",
						Interrupt:   tc.interrupt,
					}, nil
				}))

			responded := make(chan map[string]any, 1)
			go func() {
				sim.handleInitAndReady(t)
				sim.send(canUseToolRequest)
				responded <- sim.readStdin(t)
				sim.sendResult()
			}()

			session, err := client.Connect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()

			body := (<-responded)["response"].(map[string]any)["response"].(map[string]any)
			if body["behavior"] != "deny" {
				t.Errorf("behavior = %v", body["behavior"])
			}
			got, present := body["interrupt"]
			if tc.want {
				if got != true {
					t.Errorf("interrupt = %v, want true", got)
				}
			} else if present {
				t.Errorf("ordinary denial serialized interrupt: %v", got)
			}

			if _, err := session.Wait(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A withdrawn prompt must not be answered: the CLI has stopped waiting, and
// replying to a cancelled request_id is noise. Previously the callback ran to
// completion and its answer was written to a dead request.
func TestControlCancelRequestAbortsPendingPrompt(t *testing.T) {
	sim := newSessionSim()

	entered := make(chan struct{})
	release := make(chan struct{})
	client := NewWithExecutor(sim.bidi, WithCanUseTool(
		func(string, json.RawMessage) (*PermissionResponse, error) {
			close(entered)
			<-release // still deciding when the cancel arrives
			return &PermissionResponse{Allow: true}, nil
		}))

	handshake := make(chan struct{})
	cancelSeen := make(chan struct{})
	lines := make(chan string, 8)

	go func() {
		sim.handleInitAndReady(t)
		close(handshake)
		sim.send(canUseToolRequest)
		<-entered
		sim.send(`{"type":"control_cancel_request","request_id":"cli-1"}`)
		// readLoop processes lines in order, so observing this event proves
		// the cancel ahead of it has already been applied. Without that the
		// callback could return before the cancel lands and the test would
		// be racing its own setup.
		sim.send(`{"type":"system","subtype":"session_state_changed","session_id":"s1","state":"idle"}`)
		<-cancelSeen
		close(release)
		sim.sendResult()
	}()

	// Detached: the pipe stays open until Close, so this must not gate the
	// test. Starts only after the handshake has consumed the initialize
	// request from the shared reader.
	go func() {
		<-handshake
		for {
			line, err := sim.reader.ReadBytes('\n')
			if len(line) > 0 {
				lines <- string(line)
			}
			if err != nil {
				return
			}
		}
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var once sync.Once
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for ev := range session.Events() {
			if _, ok := ev.(*SessionStateChangedEvent); ok {
				once.Do(func() { close(cancelSeen) })
			}
		}
	}()

	if _, err := session.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	<-drained

	for {
		select {
		case line := <-lines:
			var msg map[string]any
			if json.Unmarshal([]byte(line), &msg) != nil {
				continue
			}
			if msg["type"] != "control_response" {
				continue
			}
			resp, _ := msg["response"].(map[string]any)
			if resp["request_id"] == "cli-1" {
				t.Fatalf("answered a cancelled request: %#v", resp)
			}
		default:
			return
		}
	}
}
