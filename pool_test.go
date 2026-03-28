package claudecli

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

// poolSim creates a connected session backed by a BidiFixtureExecutor.
// Returns the session, the sim for driving it, and the session ID.
func poolSim(t *testing.T, sessionID string) (*Session, *sessionSim) {
	t.Helper()
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInit(t)
		sim.send(`{"type":"system","session_id":"` + sessionID + `","model":"sonnet"}`)
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Drain the InitEvent so the session ID is set.
	drainUntilInit(t, session)
	if got := session.SessionID(); got != sessionID {
		t.Fatalf("session ID = %q, want %q", got, sessionID)
	}
	return session, sim
}

// drainUntilInit reads events until an InitEvent is received.
func drainUntilInit(t *testing.T, s *Session) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				t.Fatal("events closed before InitEvent")
			}
			if _, isInit := ev.(*InitEvent); isInit {
				return
			}
		case <-timeout:
			t.Fatal("timeout waiting for InitEvent")
		}
	}
}

func TestPoolAddAndEvents(t *testing.T) {
	s1, sim1 := poolSim(t, "sess-1")
	s2, sim2 := poolSim(t, "sess-2")

	pool := NewPool()
	defer pool.Close()

	if err := pool.Add(s1, SessionMeta{Name: "agent-1"}); err != nil {
		t.Fatal(err)
	}
	if err := pool.Add(s2, SessionMeta{Name: "agent-2"}); err != nil {
		t.Fatal(err)
	}

	// Send text events from both sessions.
	sim1.send(`{"type":"assistant","message":{"content":[{"type":"text","text":"hello from 1"}]}}`)
	sim2.send(`{"type":"assistant","message":{"content":[{"type":"text","text":"hello from 2"}]}}`)

	seen := map[string]bool{}
	timeout := time.After(2 * time.Second)
	for len(seen) < 2 {
		select {
		case pe := <-pool.Events():
			if te, ok := pe.Event.(*TextEvent); ok {
				seen[pe.SessionID] = true
				_ = te
			}
		case <-timeout:
			t.Fatalf("timeout: saw events from %d sessions, want 2", len(seen))
		}
	}

	if !seen["sess-1"] || !seen["sess-2"] {
		t.Errorf("missing session events: %v", seen)
	}
}

func TestPoolRemove(t *testing.T) {
	s1, _ := poolSim(t, "sess-rm")

	pool := NewPool()
	defer pool.Close()

	if err := pool.Add(s1, SessionMeta{Name: "removable"}); err != nil {
		t.Fatal(err)
	}
	if err := pool.Remove("sess-rm"); err != nil {
		t.Fatal(err)
	}

	entries := pool.List()
	if len(entries) != 0 {
		t.Errorf("List() returned %d entries after Remove, want 0", len(entries))
	}

	// Remove again should error.
	if err := pool.Remove("sess-rm"); err == nil {
		t.Error("Remove on unknown session should error")
	}
}

func TestPoolGet(t *testing.T) {
	s1, _ := poolSim(t, "sess-get")

	pool := NewPool()
	defer pool.Close()

	meta := SessionMeta{Name: "finder", Labels: map[string]string{"env": "test"}}
	if err := pool.Add(s1, meta); err != nil {
		t.Fatal(err)
	}

	session, got, ok := pool.Get("sess-get")
	if !ok {
		t.Fatal("Get returned false")
	}
	if session != s1 {
		t.Error("Get returned wrong session")
	}
	if got.Name != "finder" {
		t.Errorf("meta.Name = %q, want %q", got.Name, "finder")
	}
	if got.Labels["env"] != "test" {
		t.Errorf("meta.Labels[env] = %q, want %q", got.Labels["env"], "test")
	}

	_, _, ok = pool.Get("nonexistent")
	if ok {
		t.Error("Get for nonexistent should return false")
	}
}

func TestPoolList(t *testing.T) {
	s1, _ := poolSim(t, "sess-list-1")
	s2, _ := poolSim(t, "sess-list-2")

	pool := NewPool()
	defer pool.Close()

	pool.Add(s1, SessionMeta{Name: "a"})
	pool.Add(s2, SessionMeta{Name: "b"})

	entries := pool.List()
	if len(entries) != 2 {
		t.Fatalf("List() returned %d entries, want 2", len(entries))
	}

	names := map[string]bool{}
	for _, e := range entries {
		names[e.Meta.Name] = true
	}
	if !names["a"] || !names["b"] {
		t.Errorf("missing entries: %v", names)
	}
}

func TestPoolClose(t *testing.T) {
	s1, _ := poolSim(t, "sess-close")

	pool := NewPool()
	pool.Add(s1, SessionMeta{Name: "closeable"})
	pool.Close()

	// Events channel should be closed.
	_, ok := <-pool.Events()
	if ok {
		t.Error("Events() should be closed after Close()")
	}

	// Double close should not panic.
	pool.Close()

	// Add after close should error.
	s2, _ := poolSim(t, "sess-close-2")
	if err := pool.Add(s2, SessionMeta{Name: "late"}); err == nil {
		t.Error("Add after Close should error")
	}
}

func TestPoolConcurrentAddRemove(t *testing.T) {
	pool := NewPool()
	defer pool.Close()

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := fmt.Sprintf("sess-conc-%d", n)
			s, _ := poolSim(t, id)
			_ = pool.Add(s, SessionMeta{Name: "conc"})
		}(i)
	}
	wg.Wait()

	// Remove all.
	for _, e := range pool.List() {
		pool.Remove(e.Session.SessionID())
	}

	if len(pool.List()) != 0 {
		t.Error("expected empty pool after removing all")
	}
}

func TestPoolSessionClose(t *testing.T) {
	s1, sim1 := poolSim(t, "sess-alive")
	s2, sim2 := poolSim(t, "sess-dies")

	pool := NewPool()
	defer pool.Close()

	pool.Add(s1, SessionMeta{Name: "alive"})
	pool.Add(s2, SessionMeta{Name: "dies"})

	// Close the dying session's stdout — its events channel will close.
	sim2.send(`{"type":"result","subtype":"success","session_id":"sess-dies","total_cost_usd":0,"usage":{"input_tokens":0,"output_tokens":0}}`)
	sim2.bidi.StdoutWriter.Close()

	// Send a text event from the alive session. It may arrive before or after
	// the dying session's events drain — just keep reading until we see it.
	sim1.send(`{"type":"assistant","message":{"content":[{"type":"text","text":"still here"}]}}`)

	timeout := time.After(2 * time.Second)
	for {
		select {
		case pe := <-pool.Events():
			if pe.SessionID == "sess-alive" {
				if _, ok := pe.Event.(*TextEvent); ok {
					return // success
				}
			}
		case <-timeout:
			t.Fatal("timeout waiting for event from alive session")
		}
	}
}

func TestPoolDuplicateAdd(t *testing.T) {
	s1, _ := poolSim(t, "sess-dup")

	pool := NewPool()
	defer pool.Close()

	if err := pool.Add(s1, SessionMeta{Name: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := pool.Add(s1, SessionMeta{Name: "second"}); err == nil {
		t.Error("duplicate Add should error")
	}
}

func TestPoolAddNoSessionID(t *testing.T) {
	// Create a session that hasn't received an InitEvent yet.
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go sim.handleInit(t)
	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	pool := NewPool()
	defer pool.Close()

	if err := pool.Add(session, SessionMeta{Name: "no-id"}); err == nil {
		t.Error("Add with empty SessionID should error")
	}
}

func TestFormatAgentMessage(t *testing.T) {
	got := FormatAgentMessage("refactor-auth", "I finished the middleware")
	want := "[Message from agent \"refactor-auth\"]\nI finished the middleware\n[End of agent message]"
	if got != want {
		t.Errorf("FormatAgentMessage:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestSendAgentMessage(t *testing.T) {
	s1, _ := poolSim(t, "sess-from")
	s2, sim2 := poolSim(t, "sess-to")

	pool := NewPool()
	defer pool.Close()

	pool.Add(s1, SessionMeta{Name: "sender-agent"})
	pool.Add(s2, SessionMeta{Name: "receiver-agent"})

	// Drain stdin concurrently — io.Pipe is synchronous so writes block
	// until the read side consumes. Close the pipe at test end to unblock.
	t.Cleanup(func() { sim2.bidi.StdinReader.Close() })

	type stdinMsg struct {
		msg map[string]any
	}
	msgCh := make(chan stdinMsg, 4)
	go func() {
		for {
			msg := sim2.readStdin(t)
			if msg == nil {
				return
			}
			msgCh <- stdinMsg{msg}
		}
	}()

	// Put session into Running state so SendMessage is accepted.
	s2.Query("do something")
	// Drain the query user message.
	<-msgCh

	if err := pool.SendAgentMessage("sess-from", "sess-to", "hello peer"); err != nil {
		t.Fatalf("SendAgentMessage: %v", err)
	}

	// Read the agent message.
	select {
	case sm := <-msgCh:
		msg := sm.msg
		content, ok := msg["message"].(map[string]any)
		if !ok {
			t.Fatalf("expected message field, got %v", msg)
		}
		role, _ := content["role"].(string)
		if role != "user" {
			t.Errorf("role = %q, want %q", role, "user")
		}
		msgContent, _ := content["content"].(string)
		want := FormatAgentMessage("sender-agent", "hello peer")
		if msgContent != want {
			t.Errorf("message content:\ngot:  %q\nwant: %q", msgContent, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for agent message")
	}
}

func TestSendAgentMessageUnknown(t *testing.T) {
	s1, _ := poolSim(t, "sess-known")

	pool := NewPool()
	defer pool.Close()

	pool.Add(s1, SessionMeta{Name: "known"})

	if err := pool.SendAgentMessage("nonexistent", "sess-known", "hi"); err == nil {
		t.Error("expected error for unknown sender")
	}
	if err := pool.SendAgentMessage("sess-known", "nonexistent", "hi"); err == nil {
		t.Error("expected error for unknown target")
	}
}

func TestParseAgentInput(t *testing.T) {
	input := `{"description":"search code","prompt":"find auth","subagent_type":"Explore","name":"explorer","run_in_background":true,"model":"sonnet"}`
	ev := &ToolUseEvent{
		ID:    "tu_01",
		Name:  "Agent",
		Input: json.RawMessage(input),
	}

	got := ev.ParseAgentInput()
	if got == nil {
		t.Fatal("ParseAgentInput returned nil")
	}
	if got.Description != "search code" {
		t.Errorf("Description = %q", got.Description)
	}
	if got.Prompt != "find auth" {
		t.Errorf("Prompt = %q", got.Prompt)
	}
	if got.SubagentType != "Explore" {
		t.Errorf("SubagentType = %q", got.SubagentType)
	}
	if got.Name != "explorer" {
		t.Errorf("Name = %q", got.Name)
	}
	if !got.RunInBackground {
		t.Error("RunInBackground = false")
	}
	if got.Model != "sonnet" {
		t.Errorf("Model = %q", got.Model)
	}
}

func TestParseAgentInputNonAgent(t *testing.T) {
	ev := &ToolUseEvent{
		ID:    "tu_01",
		Name:  "Bash",
		Input: json.RawMessage(`{"command":"ls"}`),
	}
	if got := ev.ParseAgentInput(); got != nil {
		t.Errorf("expected nil for non-Agent tool, got %+v", got)
	}
}

func TestParseAgentInputMalformed(t *testing.T) {
	ev := &ToolUseEvent{
		ID:    "tu_01",
		Name:  "Agent",
		Input: json.RawMessage(`{not json`),
	}
	if got := ev.ParseAgentInput(); got != nil {
		t.Errorf("expected nil for malformed input, got %+v", got)
	}
}

func TestParseInitEventMetadata(t *testing.T) {
	events := collectEvents(t, "testdata/basic.jsonl")
	if len(events) == 0 {
		t.Fatal("no events")
	}

	init, ok := events[0].(*InitEvent)
	if !ok {
		t.Fatalf("first event is %T, want *InitEvent", events[0])
	}

	// Verify agents from testdata/basic.jsonl.
	if len(init.Agents) == 0 {
		t.Fatal("Agents is empty")
	}
	agentSet := map[string]bool{}
	for _, a := range init.Agents {
		agentSet[a] = true
	}
	for _, want := range []string{"general-purpose", "Explore", "Plan"} {
		if !agentSet[want] {
			t.Errorf("Agents missing %q", want)
		}
	}

	// Verify skills.
	if len(init.Skills) == 0 {
		t.Fatal("Skills is empty")
	}

	// Verify MCP servers.
	if len(init.MCPServers) != 3 {
		t.Fatalf("MCPServers len = %d, want 3", len(init.MCPServers))
	}
	found := false
	for _, s := range init.MCPServers {
		if s.Name == "playwright" && s.Status == "connected" {
			found = true
		}
	}
	if !found {
		t.Error("MCPServers missing playwright with status connected")
	}
}
