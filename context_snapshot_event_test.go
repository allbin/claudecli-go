package claudecli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// streamEventLine wraps an inner stream event payload in the CLI's
// stream_event envelope.
func streamEventLine(sessionID, inner string) string {
	return `{"type":"stream_event","uuid":"u","session_id":"` + sessionID + `","event":` + inner + `}`
}

const (
	msgStartOpus = `{"type":"message_start","message":{"model":"claude-opus-4-20250514","usage":{"input_tokens":100,"cache_read_input_tokens":5000,"cache_creation_input_tokens":200}}}`
	msgDelta42   = `{"type":"message_delta","usage":{"output_tokens":42}}`
)

func collect(t *testing.T, input string) []Event {
	t.Helper()
	ch := make(chan Event, 128)
	go func() {
		ParseEvents(context.Background(), strings.NewReader(input), ch)
		close(ch)
	}()
	var evs []Event
	for e := range ch {
		evs = append(evs, e)
	}
	return evs
}

func contextSnapshotEvents(evs []Event) []*ContextSnapshotEvent {
	var out []*ContextSnapshotEvent
	for _, e := range evs {
		if cs, ok := e.(*ContextSnapshotEvent); ok {
			out = append(out, cs)
		}
	}
	return out
}

// ParseEvents returns at the terminal result event, so it never observes a
// context window while stream events are still arriving. The documented
// consequence is that it emits no ContextSnapshotEvent at all.
func TestContextSnapshotEventWithheldInParseEvents(t *testing.T) {
	input := `{"type":"system","session_id":"s1","model":"opus"}
` + streamEventLine("s1", msgStartOpus) + `
` + streamEventLine("s1", msgDelta42) + `
{"type":"result","subtype":"success","modelUsage":{"claude-opus-4-20250514":{"contextWindow":200000}}}
`
	got := contextSnapshotEvents(collect(t, input))
	if len(got) != 0 {
		t.Fatalf("got %d ContextSnapshotEvent, want 0 (window never known mid-stream): %v", len(got), got)
	}
}

// The withholding must not disturb the turn-end snapshot on ResultEvent.
func TestContextSnapshotEventDoesNotDisturbResult(t *testing.T) {
	input := `{"type":"system","session_id":"s1","model":"opus"}
` + streamEventLine("s1", msgStartOpus) + `
` + streamEventLine("s1", msgDelta42) + `
{"type":"result","subtype":"success","modelUsage":{"claude-opus-4-20250514":{"contextWindow":200000}}}
`
	var result *ResultEvent
	for _, e := range collect(t, input) {
		if r, ok := e.(*ResultEvent); ok {
			result = r
		}
	}
	if result == nil || result.ContextSnapshot == nil {
		t.Fatal("no ResultEvent with a ContextSnapshot")
	}
	if got := result.ContextSnapshot.Used(); got != 5342 {
		t.Errorf("Used() = %d, want 5342", got)
	}
	if result.ContextSnapshot.ContextWindow != 200000 {
		t.Errorf("ContextWindow = %d, want 200000", result.ContextSnapshot.ContextWindow)
	}
}

// Once the window is known, a message_start / message_delta pair produces
// exactly two events, in that order, with the output only settled on the final.
func TestContextTrackerEmitsPairOnceWindowKnown(t *testing.T) {
	var tr contextTracker
	tr.learnWindow("claude-opus-4-20250514", 200000)

	start := tr.observe(json.RawMessage(msgStartOpus), "s1")
	if start == nil {
		t.Fatal("no event for message_start")
	}
	want := ContextSnapshotEvent{
		InputTokens:              100,
		CacheReadInputTokens:     5000,
		CacheCreationInputTokens: 200,
		OutputTokens:             0,
		ContextWindow:            200000,
		Model:                    "claude-opus-4-20250514",
		SessionID:                "s1",
		Phase:                    ContextSnapshotStart,
	}
	if *start != want {
		t.Errorf("message_start event =\n %+v\nwant\n %+v", *start, want)
	}
	if start.Used() != 5300 {
		t.Errorf("Used() = %d, want 5300", start.Used())
	}

	final := tr.observe(json.RawMessage(msgDelta42), "s1")
	if final == nil {
		t.Fatal("no event for message_delta")
	}
	want.OutputTokens = 42
	want.Phase = ContextSnapshotFinal
	if *final != want {
		t.Errorf("message_delta event =\n %+v\nwant\n %+v", *final, want)
	}
}

// Inner events that carry no usage must stay silent, and a message_delta with
// no preceding message_start has nothing to report.
func TestContextTrackerIgnoresIrrelevantInnerEvents(t *testing.T) {
	var tr contextTracker
	tr.learnWindow("claude-opus-4-20250514", 200000)

	for _, inner := range []string{
		``,
		`not json`,
		`{"type":"content_block_delta","delta":{"text":"hi"}}`,
		`{"type":"message_stop"}`,
		msgDelta42, // no message_start yet
	} {
		if ev := tr.observe(json.RawMessage(inner), "s1"); ev != nil {
			t.Errorf("observe(%q) = %+v, want nil", inner, ev)
		}
	}
}

// The window is learned from ModelUsage keys that carry a "[1m]"-style suffix
// while inner stream events name the model bare.
func TestContextTrackerWindowSuffixMatch(t *testing.T) {
	var tr contextTracker
	tr.learnWindows(map[string]ModelUsage{
		"claude-opus-4-6[1m]":        {ContextWindow: 1000000},
		"claude-haiku-4-5-20251001":  {ContextWindow: 200000},
		"model-with-no-window-known": {},
	})
	if got := tr.windowFor("claude-opus-4-6"); got != 1000000 {
		t.Errorf("windowFor(bare name) = %d, want 1000000", got)
	}
	if got := tr.windowFor("claude-haiku-4-5-20251001"); got != 200000 {
		t.Errorf("windowFor(exact) = %d, want 200000", got)
	}
	if got := tr.windowFor("model-with-no-window-known"); got != 0 {
		t.Errorf("windowFor(zero window) = %d, want 0", got)
	}
	if got := tr.windowFor("claude-sonnet-9"); got != 0 {
		t.Errorf("windowFor(unknown) = %d, want 0", got)
	}
}

// finish resets the in-flight measurement but must keep the learned windows —
// the window is a property of the model, not of the turn. That is what lets
// turn two of a Session emit mid-turn events.
func TestContextTrackerRemembersWindowAcrossTurns(t *testing.T) {
	var tr contextTracker

	// Turn one: withheld, window unknown until the result.
	if ev := tr.observe(json.RawMessage(msgStartOpus), "s1"); ev != nil {
		t.Fatalf("turn 1 message_start emitted %+v, want nil", ev)
	}
	snapshot := tr.finish(map[string]ModelUsage{
		"claude-opus-4-20250514": {ContextWindow: 200000},
	})
	if snapshot == nil || snapshot.ContextWindow != 200000 {
		t.Fatalf("turn 1 result snapshot = %+v, want ContextWindow 200000", snapshot)
	}
	if tr.snapshot != nil || tr.lastModel != "" {
		t.Error("finish did not reset the in-flight measurement")
	}

	// Turn two: the window survived, so the very first inner event emits.
	ev := tr.observe(json.RawMessage(msgStartOpus), "s1")
	if ev == nil {
		t.Fatal("turn 2 message_start emitted nothing, want an event")
	}
	if ev.ContextWindow != 200000 {
		t.Errorf("ContextWindow = %d, want 200000", ev.ContextWindow)
	}

	// A session init resets the turn but likewise keeps the window.
	tr.reset()
	if ev := tr.observe(json.RawMessage(msgStartOpus), "s1"); ev == nil {
		t.Error("reset dropped the learned window")
	}
}

// externalWindow is the escape hatch that lets Session.QueryContextUsage
// unblock the first turn.
func TestContextTrackerExternalWindow(t *testing.T) {
	tr := contextTracker{
		externalWindow: func(model string) int {
			if model == "claude-opus-4-20250514" {
				return 1000000
			}
			return 0
		},
	}
	ev := tr.observe(json.RawMessage(msgStartOpus), "s1")
	if ev == nil {
		t.Fatal("no event, want one seeded from externalWindow")
	}
	if ev.ContextWindow != 1000000 {
		t.Errorf("ContextWindow = %d, want 1000000", ev.ContextWindow)
	}

	// A window learned from the stream wins over the external source.
	tr.learnWindow("claude-opus-4-20250514", 200000)
	ev = tr.observe(json.RawMessage(msgStartOpus), "s1")
	if ev.ContextWindow != 200000 {
		t.Errorf("ContextWindow = %d, want the stream-learned 200000", ev.ContextWindow)
	}
}

func TestSessionObservedContextWindow(t *testing.T) {
	s := &Session{}
	s.recordContextWindow(&ContextUsage{
		Model:        "claude-opus-5[1m]",
		MaxTokens:    200000,
		RawMaxTokens: 1000000,
	})
	// RawMaxTokens, not the smaller compaction-policy MaxTokens.
	if got := s.observedContextWindow("claude-opus-5[1m]"); got != 1000000 {
		t.Errorf("observedContextWindow(exact) = %d, want 1000000", got)
	}
	// Inner stream events name the model bare.
	if got := s.observedContextWindow("claude-opus-5"); got != 1000000 {
		t.Errorf("observedContextWindow(bare) = %d, want 1000000", got)
	}
	if got := s.observedContextWindow("claude-haiku-4-5"); got != 0 {
		t.Errorf("observedContextWindow(unknown) = %d, want 0", got)
	}
	if got := s.observedContextWindow(""); got != 0 {
		t.Errorf("observedContextWindow(empty) = %d, want 0", got)
	}
}

func TestSessionObservedContextWindowSuffixOnTheStoredKey(t *testing.T) {
	s := &Session{}
	s.recordContextWindow(&ContextUsage{Model: "claude-opus-5", RawMaxTokens: 1000000})
	if got := s.observedContextWindow("claude-opus-5[1m]"); got != 1000000 {
		t.Errorf("observedContextWindow(suffixed query) = %d, want 1000000", got)
	}
}

func TestSessionRecordContextWindowIgnoresUnusable(t *testing.T) {
	s := &Session{}
	s.recordContextWindow(nil)
	s.recordContextWindow(&ContextUsage{Model: "", RawMaxTokens: 1000})
	s.recordContextWindow(&ContextUsage{Model: "m", RawMaxTokens: 0, MaxTokens: 0})
	if got := s.observedContextWindow("m"); got != 0 {
		t.Errorf("observedContextWindow = %d, want 0", got)
	}
	// MaxTokens is the fallback when the CLI omits RawMaxTokens.
	s.recordContextWindow(&ContextUsage{Model: "m", MaxTokens: 400000})
	if got := s.observedContextWindow("m"); got != 400000 {
		t.Errorf("observedContextWindow = %d, want 400000", got)
	}
}

func TestContextSnapshotEventString(t *testing.T) {
	ev := &ContextSnapshotEvent{
		InputTokens:   10,
		OutputTokens:  5,
		ContextWindow: 200000,
		Model:         "claude-opus-4-20250514",
		Phase:         ContextSnapshotFinal,
	}
	want := "ContextSnapshotEvent{final: 15/200000, model: claude-opus-4-20250514}"
	if got := ev.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

// The Session path is the one that can actually emit: it stays open across
// turns, so the window disclosed by turn one's result unblocks turn two.
func TestSessionEmitsContextSnapshotEventsOnLaterTurns(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)
		// Turn one: stream events arrive before any window is known.
		sim.readStdin(t)
		sim.send(streamEventLine("test-sess", msgStartOpus))
		sim.send(streamEventLine("test-sess", msgDelta42))
		sim.send(`{"type":"result","subtype":"success","session_id":"test-sess","total_cost_usd":0.01,"usage":{"input_tokens":1,"output_tokens":1},"modelUsage":{"claude-opus-4-20250514":{"contextWindow":200000}}}`)
		// Turn two: same stream events, window now known.
		sim.readStdin(t)
		sim.send(streamEventLine("test-sess", msgStartOpus))
		sim.send(streamEventLine("test-sess", msgDelta42))
		sim.send(`{"type":"result","subtype":"success","session_id":"test-sess","total_cost_usd":0.01,"usage":{"input_tokens":1,"output_tokens":1},"modelUsage":{"claude-opus-4-20250514":{"contextWindow":200000}}}`)
		sim.bidi.StdoutWriter.Close()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	var turn1, turn2 []*ContextSnapshotEvent
	results := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range session.Events() {
			switch e := ev.(type) {
			case *ContextSnapshotEvent:
				if results == 0 {
					turn1 = append(turn1, e)
				} else {
					turn2 = append(turn2, e)
				}
			case *ResultEvent:
				results++
			}
		}
	}()

	if err := session.Query("one"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := session.Query("two"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Wait(); err != nil {
		t.Fatal(err)
	}
	// Let the stream drain on its own (the sim closed stdout after turn two);
	// closing here could cut off events already queued behind the result.
	<-done

	if len(turn1) != 0 {
		t.Errorf("turn 1 emitted %d ContextSnapshotEvent, want 0 (window not yet known)", len(turn1))
	}
	if len(turn2) != 2 {
		t.Fatalf("turn 2 emitted %d ContextSnapshotEvent, want 2 (one pair): %v", len(turn2), turn2)
	}
	if turn2[0].Phase != ContextSnapshotStart || turn2[1].Phase != ContextSnapshotFinal {
		t.Errorf("phases = %s, %s; want %s, %s", turn2[0].Phase, turn2[1].Phase, ContextSnapshotStart, ContextSnapshotFinal)
	}
	if turn2[0].OutputTokens != 0 || turn2[1].OutputTokens != 42 {
		t.Errorf("OutputTokens = %d, %d; want 0, 42", turn2[0].OutputTokens, turn2[1].OutputTokens)
	}
	for i, ev := range turn2 {
		if ev.ContextWindow != 200000 {
			t.Errorf("turn2[%d].ContextWindow = %d, want 200000", i, ev.ContextWindow)
		}
		if ev.SessionID != "test-sess" {
			t.Errorf("turn2[%d].SessionID = %q, want %q", i, ev.SessionID, "test-sess")
		}
		if ev.Model != "claude-opus-4-20250514" {
			t.Errorf("turn2[%d].Model = %q", i, ev.Model)
		}
	}
}
