//go:build integration

package claudecli

import (
	"context"
	"testing"
	"time"
)

// TestIntegrationContextSnapshotEvents drives a real CLI over two turns and
// checks that ContextSnapshotEvent actually arrives mid-turn — before the
// ResultEvent that closes the turn — with plausible numbers.
//
// It also pins the empirical claim the withhold-until-known design rests on:
// the CLI does not disclose a context window mid-turn, so turn one is silent
// and turn two is not.
func TestIntegrationContextSnapshotEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	client := New(
		WithModel(ModelHaiku),
		WithIncludePartialMessages(),
		WithPermissionMode(PermissionBypass),
	)
	session, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer session.Close()

	type turnObservations struct {
		beforeResult []*ContextSnapshotEvent
		afterResult  int
		sawResult    bool
	}

	// runTurn sends a prompt and collects the context events that arrive
	// before the turn's ResultEvent.
	runTurn := func(t *testing.T, prompt string) turnObservations {
		t.Helper()
		if err := session.Query(prompt); err != nil {
			t.Fatalf("Query: %v", err)
		}
		var obs turnObservations
		for {
			select {
			case <-ctx.Done():
				t.Fatalf("timed out waiting for ResultEvent")
			case ev, ok := <-session.Events():
				if !ok {
					t.Fatal("event channel closed before ResultEvent")
				}
				switch e := ev.(type) {
				case *ContextSnapshotEvent:
					if obs.sawResult {
						obs.afterResult++
					} else {
						obs.beforeResult = append(obs.beforeResult, e)
					}
				case *ErrorEvent:
					if e.Fatal {
						t.Fatalf("fatal error: %v", e.Err)
					}
				case *ResultEvent:
					obs.sawResult = true
					if e.ContextSnapshot == nil {
						t.Error("ResultEvent.ContextSnapshot is nil despite WithIncludePartialMessages")
					} else {
						t.Logf("turn result snapshot: used=%d window=%d",
							e.ContextSnapshot.Used(), e.ContextSnapshot.ContextWindow)
					}
					return obs
				}
			}
		}
	}

	one := runTurn(t, "Reply with the single word: alpha")
	t.Logf("turn 1: %d ContextSnapshotEvent before result", len(one.beforeResult))
	if len(one.beforeResult) != 0 {
		// Not a hard failure: if a future CLI starts disclosing the window
		// mid-turn this is a welcome improvement, but the doc comment and the
		// agentkit adapter both describe the silent first turn, so shout.
		t.Errorf("turn 1 emitted %d ContextSnapshotEvent; the design assumes 0 "+
			"because no window is known yet — has the CLI started reporting a "+
			"window mid-turn? Revisit ContextSnapshotEvent's doc comment.",
			len(one.beforeResult))
	}

	two := runTurn(t, "Reply with the single word: beta")
	t.Logf("turn 2: %d ContextSnapshotEvent before result", len(two.beforeResult))
	if len(two.beforeResult) < 2 {
		t.Fatalf("turn 2 emitted %d ContextSnapshotEvent before the result, want at least 2 "+
			"(a message_start/message_delta pair)", len(two.beforeResult))
	}

	var starts, finals int
	for i, ev := range two.beforeResult {
		t.Logf("  [%d] %s", i, ev)
		if ev.ContextWindow <= 0 {
			t.Errorf("event %d: ContextWindow = %d, want > 0 — the event should have been withheld", i, ev.ContextWindow)
		}
		if ev.Used() <= 0 {
			t.Errorf("event %d: Used() = %d, want > 0", i, ev.Used())
		}
		if ev.Used() > ev.ContextWindow {
			t.Errorf("event %d: Used() = %d exceeds ContextWindow %d", i, ev.Used(), ev.ContextWindow)
		}
		if ev.Model == "" {
			t.Errorf("event %d: empty Model", i)
		}
		if ev.SessionID == "" {
			t.Errorf("event %d: empty SessionID", i)
		}
		switch ev.Phase {
		case ContextSnapshotStart:
			starts++
			if ev.OutputTokens != 0 {
				t.Errorf("event %d: start phase carries OutputTokens = %d, want 0", i, ev.OutputTokens)
			}
		case ContextSnapshotFinal:
			finals++
		default:
			t.Errorf("event %d: unknown Phase %q", i, ev.Phase)
		}
	}
	if starts == 0 || finals == 0 {
		t.Errorf("phases seen: %d start, %d final; want at least one of each", starts, finals)
	}

	// Turn three uses a tool, so the turn spans several API calls. The point of
	// the whole feature is that the meter moves repeatedly within one turn, not
	// just once — so assert more than one pair.
	three := runTurn(t, "Run `echo delta` with the Bash tool, then reply with just its output.")
	t.Logf("turn 3 (tool-using): %d ContextSnapshotEvent before result", len(three.beforeResult))
	for i, ev := range three.beforeResult {
		t.Logf("  [%d] %s", i, ev)
	}
	if len(three.beforeResult) < 4 {
		t.Errorf("tool-using turn emitted %d ContextSnapshotEvent, want at least 4 "+
			"(one start/final pair per API call, and a tool call means at least two calls)",
			len(three.beforeResult))
	}
	// Occupancy only grows within a turn: each API call re-sends the transcript
	// plus whatever the last call added.
	for i := 1; i < len(three.beforeResult); i++ {
		prev, cur := three.beforeResult[i-1], three.beforeResult[i]
		if cur.Phase == ContextSnapshotStart && cur.Used() < prev.Used() {
			t.Errorf("event %d: Used() dropped from %d to %d across API calls", i, prev.Used(), cur.Used())
		}
	}
}

// TestIntegrationContextSnapshotEventsFirstTurnViaQueryContextUsage checks the
// escape hatch: asking the CLI for context usage teaches the window, so even
// the first turn of a session emits mid-turn events.
func TestIntegrationContextSnapshotEventsFirstTurnViaQueryContextUsage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client := New(WithModel(ModelHaiku), WithIncludePartialMessages())
	session, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer session.Close()

	usage, err := session.QueryContextUsage()
	if err != nil {
		t.Fatalf("QueryContextUsage: %v", err)
	}
	t.Logf("context usage: model=%q maxTokens=%d rawMaxTokens=%d", usage.Model, usage.MaxTokens, usage.RawMaxTokens)
	if usage.Model == "" {
		t.Fatal("get_context_usage returned no model — cannot seed the window")
	}

	if err := session.Query("Reply with the single word: gamma"); err != nil {
		t.Fatalf("Query: %v", err)
	}
	var mid []*ContextSnapshotEvent
	for done := false; !done; {
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for ResultEvent")
		case ev, ok := <-session.Events():
			if !ok {
				t.Fatal("event channel closed before ResultEvent")
			}
			switch e := ev.(type) {
			case *ContextSnapshotEvent:
				mid = append(mid, e)
			case *ResultEvent:
				done = true
			case *ErrorEvent:
				if e.Fatal {
					t.Fatalf("fatal error: %v", e.Err)
				}
			}
		}
	}
	if len(mid) == 0 {
		t.Fatal("first turn emitted no ContextSnapshotEvent despite QueryContextUsage seeding the window")
	}
	for i, ev := range mid {
		t.Logf("  [%d] %s", i, ev)
		if ev.ContextWindow <= 0 {
			t.Errorf("event %d: ContextWindow = %d, want > 0", i, ev.ContextWindow)
		}
	}
}
