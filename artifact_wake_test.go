package claudecli

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The artifact_wake_*.jsonl fixtures are CLI 2.1.282 captures of a comment
// sent to Claude on an artifact the session watches, trimmed of stream_event,
// thinking_tokens, status, rate-limit and control lines, with init lines
// shortened.
//
//   - plan: a turn arms the watch (prompt echoed), then a comment wakes the
//     idle session in plan mode. The CLI enqueues that notice without a uuid,
//     as it does when auto-reply is notify-only, so the wake opens with a
//     bare init: no command_lifecycle, no replayed user message.
//   - lifecycle: the wake alone, from default mode where the CLI auto-replied
//     first. That notice has a uuid, so command_lifecycle started precedes
//     the init.

// initsAndResults reads events until n results have arrived.
func initsAndResults(t *testing.T, ch <-chan Event, n int) ([]*InitEvent, []*ResultEvent) {
	t.Helper()
	var inits []*InitEvent
	var rs []*ResultEvent
	deadline := time.After(2 * time.Second)
	for len(rs) < n {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("events closed after %d results, want %d", len(rs), n)
			}
			switch e := ev.(type) {
			case *InitEvent:
				inits = append(inits, e)
			case *ResultEvent:
				rs = append(rs, e)
			}
		case <-deadline:
			t.Fatalf("timeout after %d results, want %d", len(rs), n)
		}
	}
	return inits, rs
}

func unsolicitedInits(inits []*InitEvent) []bool {
	out := make([]bool, len(inits))
	for i, e := range inits {
		out[i] = e.Unsolicited
	}
	return out
}

func TestSessionInitUnsolicitedArtifactWake(t *testing.T) {
	lines := fixtureLines(t, "artifact_wake_plan.jsonl")
	arm, wake := splitAfterResults(t, lines, 1)

	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)
	go func() {
		sim.handleInit(t)
		sim.readStdin(t)
		for _, l := range arm {
			sim.send(l)
		}
		for _, l := range wake {
			sim.send(l)
		}
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	defer sim.bidi.StdoutWriter.Close()

	if err := session.QueryMsg(Message{Text: "Watch the comments", FromHuman: true}); err != nil {
		t.Fatal(err)
	}
	inits, rs := initsAndResults(t, session.Events(), 2)
	if got := unsolicitedInits(inits); len(got) != 2 || got[0] || !got[1] {
		t.Fatalf("init Unsolicited = %v, want [false true]", got)
	}
	if rs[0].Unsolicited || !rs[1].Unsolicited {
		t.Errorf("result Unsolicited = [%v %v], want [false true]", rs[0].Unsolicited, rs[1].Unsolicited)
	}
	if !strings.Contains(inits[1].String(), "Unsolicited") {
		t.Errorf("String() = %s", inits[1])
	}
}

func TestSessionInitUnsolicitedLifecycleWake(t *testing.T) {
	wake := fixtureLines(t, "artifact_wake_lifecycle.jsonl")

	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi, WithReplayUserMessages())
	go func() {
		sim.handleInit(t)
		sim.readStdin(t)
		sim.send(`{"type":"system","subtype":"init","session_id":"s","model":"m"}`)
		sim.send(`{"type":"user","message":{"role":"user","content":"publish"},"session_id":"s","parent_tool_use_id":null,"isReplay":true}`)
		sim.sendAnswer("published")
		for _, l := range wake {
			sim.send(l)
		}
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	defer sim.bidi.StdoutWriter.Close()

	if err := session.Query("publish"); err != nil {
		t.Fatal(err)
	}
	inits, rs := initsAndResults(t, session.Events(), 2)
	if got := unsolicitedInits(inits); len(got) != 2 || got[0] || !got[1] {
		t.Fatalf("init Unsolicited = %v, want [false true]", got)
	}
	if !rs[1].Unsolicited {
		t.Errorf("wake result = %+v, want unsolicited", rs[1])
	}
}

// A slash command runs a turn of its own but is never echoed. Its result
// must settle it, or every later wake would look like the caller's turn.
func TestSessionInitUnsolicitedAfterSlashCommand(t *testing.T) {
	wake := fixtureLines(t, "artifact_wake_lifecycle.jsonl")

	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)
	go func() {
		sim.handleInit(t)
		sim.readStdin(t)
		sim.send(`{"type":"system","subtype":"init","session_id":"s","model":"m"}`)
		sim.sendAnswer("cost report")
		for _, l := range wake {
			sim.send(l)
		}
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	defer sim.bidi.StdoutWriter.Close()

	if err := session.Query("/cost"); err != nil {
		t.Fatal(err)
	}
	inits, _ := initsAndResults(t, session.Events(), 2)
	if got := unsolicitedInits(inits); len(got) != 2 || got[0] || !got[1] {
		t.Fatalf("init Unsolicited = %v, want [false true]", got)
	}
}

// Messages sent mid-turn start turns of their own after it, init before echo
// (CLI 2.1.282), and a queued slash command follows with no echo. None of
// those inits is unsolicited; the wake after them is.
func TestSessionInitQueuedMessagesNotUnsolicited(t *testing.T) {
	wake := fixtureLines(t, "artifact_wake_lifecycle.jsonl")
	queued := make(chan struct{})

	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)
	go func() {
		sim.handleInit(t)
		sim.readStdin(t)
		sim.send(`{"type":"system","subtype":"init","session_id":"s","model":"m"}`)
		sim.send(`{"type":"user","message":{"role":"user","content":"poem"},"session_id":"s","parent_tool_use_id":null,"isReplay":true}`)
		sim.readStdin(t)
		sim.readStdin(t)
		<-queued
		sim.sendAnswer("poem")
		sim.send(`{"type":"system","subtype":"init","session_id":"s","model":"m"}`)
		sim.send(`{"type":"user","message":{"role":"user","content":"second"},"session_id":"s","parent_tool_use_id":null,"isReplay":true}`)
		sim.sendAnswer("second")
		sim.send(`{"type":"system","subtype":"init","session_id":"s","model":"m"}`)
		sim.sendAnswer("cost report")
		for _, l := range wake {
			sim.send(l)
		}
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	defer sim.bidi.StdoutWriter.Close()

	if err := session.Query("poem"); err != nil {
		t.Fatal(err)
	}
	if err := session.SendMessage("second"); err != nil {
		t.Fatal(err)
	}
	if err := session.SendMessage("/cost"); err != nil {
		t.Fatal(err)
	}
	close(queued)
	inits, _ := initsAndResults(t, session.Events(), 4)
	if got := unsolicitedInits(inits); len(got) != 4 || got[0] || got[1] || got[2] || !got[3] {
		t.Fatalf("init Unsolicited = %v, want [false false false true]", got)
	}
}

// A notification turn that runs ahead of an unread prompt cannot be told apart
// at its init; it is not flagged. Its result still is.
func TestSessionInitNotFlaggedWhilePromptUnread(t *testing.T) {
	lines := fixtureLines(t, "task_notification_resume.jsonl")

	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)
	go func() {
		sim.handleInit(t)
		sim.readStdin(t)
		for _, l := range lines {
			sim.send(l)
		}
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	defer sim.bidi.StdoutWriter.Close()

	if err := session.Query("Reply with just the word HI."); err != nil {
		t.Fatal(err)
	}
	inits, rs := initsAndResults(t, session.Events(), 2)
	for i, e := range inits {
		if e.Unsolicited {
			t.Errorf("init %d Unsolicited with the prompt unread", i)
		}
	}
	if !rs[0].Unsolicited || rs[1].Unsolicited {
		t.Errorf("result Unsolicited = [%v %v], want [true false]", rs[0].Unsolicited, rs[1].Unsolicited)
	}
}

// Older CLIs emit an init right after initialize, before any message.
func TestSessionInitAtStartupNotUnsolicited(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)
	go sim.handleInitAndReady(t)

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	defer sim.bidi.StdoutWriter.Close()

	select {
	case ev := <-session.Events():
		init, ok := ev.(*InitEvent)
		if !ok {
			t.Fatalf("first event = %T, want *InitEvent", ev)
		}
		if init.Unsolicited {
			t.Error("startup init flagged Unsolicited")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no init")
	}
}
