package claudecli

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
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

	runCancelledPrompt(t, sim, client, entered, func() { close(release) })
}

// The ctx handed to a WithCanUseToolRequestContext callback must fire when the
// CLI withdraws the prompt. Without it a host that parks the request on a human
// leaves a dead dialog on screen: the answer is discarded either way, but only
// the ctx tells the host that.
func TestCanUseToolRequestContextCancelledOnWithdrawal(t *testing.T) {
	sim := newSessionSim()

	entered := make(chan struct{})
	ctxDone := make(chan struct{})
	gotReq := make(chan ToolPermissionRequest, 1)

	client := NewWithExecutor(sim.bidi, WithCanUseToolRequestContext(
		func(ctx context.Context, req ToolPermissionRequest) (*PermissionResponse, error) {
			gotReq <- req
			close(entered)
			// A host would drop its prompt here. Blocking on ctx.Done() is
			// how the SDK expects a parked callback to unwind.
			<-ctx.Done()
			close(ctxDone)
			return &PermissionResponse{Allow: true}, ctx.Err()
		}))

	runCancelledPrompt(t, sim, client, entered, func() {
		select {
		case <-ctxDone:
		case <-time.After(5 * time.Second):
			t.Error("callback ctx never cancelled after control_cancel_request")
		}
	})

	// The full request must still reach the ctx-shaped callback — it is the
	// strictly-more-informed variant, not a reduced one.
	req := <-gotReq
	if req.ToolUseID != "toolu_01NbjBgwHU6dbKU6ZXYiuwxS" || req.ToolName != "Write" {
		t.Errorf("request not carried through: %+v", req)
	}
}

// The other half of the contract: the ctx also ends with the session, so a
// parked callback unwinds instead of holding a dialog open forever.
func TestCanUseToolRequestContextCancelledOnSessionEnd(t *testing.T) {
	sim := newSessionSim()

	entered := make(chan struct{})
	ctxDone := make(chan struct{})
	client := NewWithExecutor(sim.bidi, WithCanUseToolRequestContext(
		func(ctx context.Context, req ToolPermissionRequest) (*PermissionResponse, error) {
			close(entered)
			<-ctx.Done()
			close(ctxDone)
			return nil, ctx.Err()
		}))

	go func() {
		sim.handleInitAndReady(t)
		sim.send(canUseToolRequest)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	session, err := client.Connect(ctx)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	go func() {
		for range session.Events() {
		}
	}()

	<-entered
	cancel()

	select {
	case <-ctxDone:
	case <-time.After(5 * time.Second):
		t.Fatal("callback ctx never cancelled after session context ended")
	}

	// Let readLoop finish: it waits on in-flight control handlers, then on EOF.
	sim.bidi.StdoutWriter.Close()
	session.Close()
}

// The same contract for AskUserQuestion, which always parks on a human.
func TestUserInputContextCancelledOnWithdrawal(t *testing.T) {
	sim := newSessionSim()

	entered := make(chan struct{})
	ctxDone := make(chan struct{})
	gotQuestions := make(chan []Question, 1)

	client := NewWithExecutor(sim.bidi, WithUserInputContext(
		func(ctx context.Context, questions []Question) (map[string]string, error) {
			gotQuestions <- questions
			close(entered)
			<-ctx.Done()
			close(ctxDone)
			return nil, ctx.Err()
		}))

	runCancelledPromptWith(t, sim, client, askUserQuestionRequest, entered, func() {
		select {
		case <-ctxDone:
		case <-time.After(5 * time.Second):
			t.Error("callback ctx never cancelled after control_cancel_request")
		}
	})

	if qs := <-gotQuestions; len(qs) != 1 || qs[0].Question != "Which database?" {
		t.Errorf("questions not carried through: %+v", qs)
	}
}

// Registering the ctx-shaped callback must not change what a non-ctx callback
// sees or answers: this is purely additive.
func TestNonContextCallbacksUnaffected(t *testing.T) {
	t.Run("canUseTool still answers", func(t *testing.T) {
		sim := newSessionSim()
		client := NewWithExecutor(sim.bidi, WithCanUseTool(
			func(name string, input json.RawMessage) (*PermissionResponse, error) {
				if name != "Write" {
					t.Errorf("toolName = %q", name)
				}
				return &PermissionResponse{Allow: true}, nil
			}))
		body := roundTripPermission(t, sim, client, canUseToolRequest)
		if body["behavior"] != "allow" {
			t.Errorf("behavior = %v", body["behavior"])
		}
	})

	t.Run("userInput still answers", func(t *testing.T) {
		sim := newSessionSim()
		client := NewWithExecutor(sim.bidi, WithUserInput(
			func(questions []Question) (map[string]string, error) {
				return map[string]string{questions[0].Question: "Postgres"}, nil
			}))
		body := roundTripPermission(t, sim, client, askUserQuestionRequest)
		updated, ok := body["updatedInput"].(map[string]any)
		if !ok {
			t.Fatalf("updatedInput missing: %#v", body)
		}
		answers, _ := updated["answers"].(map[string]any)
		if answers["Which database?"] != "Postgres" {
			t.Errorf("answers = %#v", answers)
		}
	})

	// Precedence: the more informed callback wins, and the ctx-shaped one is
	// the most informed. Registering all three must not resurrect the others.
	t.Run("ctx variant outranks both", func(t *testing.T) {
		sim := newSessionSim()
		called := make(chan string, 3)
		client := NewWithExecutor(sim.bidi,
			WithCanUseTool(func(string, json.RawMessage) (*PermissionResponse, error) {
				called <- "plain"
				return &PermissionResponse{Allow: true}, nil
			}),
			WithCanUseToolRequest(func(ToolPermissionRequest) (*PermissionResponse, error) {
				called <- "req"
				return &PermissionResponse{Allow: true}, nil
			}),
			WithCanUseToolRequestContext(func(context.Context, ToolPermissionRequest) (*PermissionResponse, error) {
				called <- "ctx"
				return &PermissionResponse{Allow: true}, nil
			}))
		roundTripPermission(t, sim, client, canUseToolRequest)
		if got := <-called; got != "ctx" {
			t.Errorf("dispatched to %q, want ctx", got)
		}
		select {
		case extra := <-called:
			t.Errorf("also dispatched to %q", extra)
		default:
		}
	})

	t.Run("userInput ctx variant outranks plain", func(t *testing.T) {
		sim := newSessionSim()
		called := make(chan string, 2)
		client := NewWithExecutor(sim.bidi,
			WithUserInput(func([]Question) (map[string]string, error) {
				called <- "plain"
				return nil, nil
			}),
			WithUserInputContext(func(context.Context, []Question) (map[string]string, error) {
				called <- "ctx"
				return nil, nil
			}))
		roundTripPermission(t, sim, client, askUserQuestionRequest)
		if got := <-called; got != "ctx" {
			t.Errorf("dispatched to %q, want ctx", got)
		}
		select {
		case extra := <-called:
			t.Errorf("also dispatched to %q", extra)
		default:
		}
	})
}

// Both ctx-shaped options must still request the permission prompt tool — a
// callback the CLI never calls is worse than no callback.
func TestContextCallbacksRequestPermissionPromptTool(t *testing.T) {
	for _, tc := range []struct {
		name string
		opt  Option
	}{
		{"canUseToolRequestContext", WithCanUseToolRequestContext(
			func(context.Context, ToolPermissionRequest) (*PermissionResponse, error) { return nil, nil })},
		{"userInputContext", WithUserInputContext(
			func(context.Context, []Question) (map[string]string, error) { return nil, nil })},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := resolveOptions(nil, []Option{tc.opt}).buildSessionArgs()
			found := false
			for i, a := range args {
				if a == "--permission-prompt-tool" && i+1 < len(args) && args[i+1] == "stdio" {
					found = true
				}
			}
			if !found {
				t.Errorf("--permission-prompt-tool stdio missing from %v", args)
			}
		})
	}
}

// Verbatim shape of an AskUserQuestion can_use_tool request (Claude CLI
// 2.1.235), trimmed to the fields the SDK reads.
const askUserQuestionRequest = `{"type":"control_request","request_id":"cli-1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Which database?","header":"Database","options":[{"label":"Postgres"},{"label":"SQLite"}]}]},"tool_use_id":"toolu_01AskUserQ"}}`

// roundTripPermission drives one can_use_tool request to completion and returns
// the decoded response body the SDK wrote back.
func roundTripPermission(t *testing.T, sim *sessionSim, client *Client, request string) map[string]any {
	t.Helper()
	responded := make(chan map[string]any, 1)
	go func() {
		sim.handleInitAndReady(t)
		sim.send(request)
		responded <- sim.readStdin(t)
		sim.sendResult()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	msg := <-responded
	resp, ok := msg["response"].(map[string]any)
	if !ok {
		t.Fatalf("no control_response: %#v", msg)
	}
	if resp["subtype"] != "success" {
		t.Fatalf("error response: %#v", resp)
	}
	body, ok := resp["response"].(map[string]any)
	if !ok {
		t.Fatalf("response body missing: %#v", resp)
	}
	if _, err := session.Wait(); err != nil {
		t.Fatal(err)
	}
	return body
}

func runCancelledPrompt(t *testing.T, sim *sessionSim, client *Client, entered <-chan struct{}, afterCancel func()) {
	t.Helper()
	runCancelledPromptWith(t, sim, client, canUseToolRequest, entered, afterCancel)
}

// runCancelledPromptWith sends request, waits for the callback to be parked in
// it, withdraws it, then asserts no control_response was written for the
// withdrawn id. afterCancel runs once the cancel is known to have been applied.
func runCancelledPromptWith(t *testing.T, sim *sessionSim, client *Client, request string, entered <-chan struct{}, afterCancel func()) {
	t.Helper()

	handshake := make(chan struct{})
	cancelSeen := make(chan struct{})
	lines := make(chan string, 8)

	go func() {
		sim.handleInitAndReady(t)
		close(handshake)
		sim.send(request)
		<-entered
		sim.send(`{"type":"control_cancel_request","request_id":"cli-1"}`)
		// readLoop processes lines in order, so observing this event proves
		// the cancel ahead of it has already been applied. Without that the
		// callback could return before the cancel lands and the test would
		// be racing its own setup.
		sim.send(`{"type":"system","subtype":"session_state_changed","session_id":"s1","state":"idle"}`)
		<-cancelSeen
		afterCancel()
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
