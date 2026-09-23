package claudecli

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// The task_notification_*.jsonl fixtures are CLI 2.1.280 captures, trimmed of
// stream_event, thinking_tokens, rate-limit and hook lines, with init lines
// shortened. All three ran with --replay-user-messages.
//
//   - resume: a --resume after the previous process died with a background
//     shell still running. The CLI reports the orphaned task, closes it with
//     an empty origin=task-notification result, and only then reads the
//     prompt ("HI").
//   - wake: a turn starts `sleep 20` in the background and ends. When the
//     task finishes the CLI wakes up by itself and emits a second result,
//     origin=task-notification, with real model output.
//   - fold: like wake, but the model runs a foreground tool in the wake-up
//     turn, and a second prompt ("PONG") sent meanwhile is folded into that
//     turn. Its echo arrives mid-turn and the turn's only result, still
//     origin=task-notification, is the answer to it.

func fixtureLines(t *testing.T, name string) []string {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// splitAfterResults splits lines after the n-th result line (1-based).
func splitAfterResults(t *testing.T, lines []string, n int) ([]string, []string) {
	t.Helper()
	seen := 0
	for i, l := range lines {
		if strings.Contains(l, `"type":"result"`) {
			seen++
			if seen == n {
				return lines[:i+1], lines[i+1:]
			}
		}
	}
	t.Fatalf("fixture has fewer than %d results", n)
	return nil, nil
}

func withoutReplays(lines []string) []string {
	return slices.DeleteFunc(slices.Clone(lines), func(l string) bool {
		return strings.Contains(l, `"isReplay":true`)
	})
}

func results(events []Event) []*ResultEvent {
	var out []*ResultEvent
	for _, ev := range events {
		if r, ok := ev.(*ResultEvent); ok {
			out = append(out, r)
		}
	}
	return out
}

func TestParseResultOrigin(t *testing.T) {
	events := parseLines(t, fixtureLines(t, "task_notification_resume.jsonl")...)
	rs := results(events)
	if len(rs) != 2 {
		t.Fatalf("got %d results, want 2 (ParseEvents must read past the notification result)", len(rs))
	}

	phantom, answer := rs[0], rs[1]
	if phantom.Origin == nil || phantom.Origin.Kind != OriginTaskNotification {
		t.Fatalf("phantom Origin = %+v, want kind %q", phantom.Origin, OriginTaskNotification)
	}
	if !phantom.Origin.IsTaskNotification() || phantom.Origin.Subkind != "" {
		t.Errorf("phantom Origin = %+v", phantom.Origin)
	}
	if string(phantom.Origin.Raw) != `{"kind":"task-notification"}` {
		t.Errorf("phantom Origin.Raw = %s", phantom.Origin.Raw)
	}
	if !phantom.Unsolicited {
		t.Error("phantom Unsolicited = false, want true")
	}
	if phantom.ResultIndex == nil || *phantom.ResultIndex != 0 {
		t.Errorf("phantom ResultIndex = %v, want 0", phantom.ResultIndex)
	}
	if phantom.NumTurns != 0 || phantom.Text != "" || phantom.Usage.OutputTokens != 0 {
		t.Errorf("phantom = %+v, want an empty zero-turn result", phantom)
	}

	if answer.Origin != nil {
		t.Errorf("answer Origin = %+v, want nil", answer.Origin)
	}
	if answer.Unsolicited {
		t.Error("answer Unsolicited = true")
	}
	if answer.Text != "HI" || answer.NumTurns != 1 || answer.StopReason != "end_turn" {
		t.Errorf("answer = %+v", answer)
	}
	if answer.ResultIndex == nil || *answer.ResultIndex != 1 {
		t.Errorf("answer ResultIndex = %v, want 1", answer.ResultIndex)
	}
	if !strings.Contains(phantom.String(), "Unsolicited") {
		t.Errorf("String() = %q", phantom.String())
	}
}

func TestParseOriginVariants(t *testing.T) {
	cases := []struct {
		raw      string
		wantKind string
		wantSub  string
		wantNil  bool
	}{
		{raw: ``, wantNil: true},
		{raw: `null`, wantNil: true},
		{raw: `{}`, wantNil: true},
		{raw: `"task-notification"`, wantNil: true},
		{raw: `{"kind":"human"}`, wantKind: OriginHuman},
		{raw: `{"kind":"task-notification","subkind":"scheduled-trigger","fireReason":"manual"}`, wantKind: OriginTaskNotification, wantSub: "scheduled-trigger"},
		{raw: `{"kind":"peer","from":"uds:/x","name":"bob"}`, wantKind: "peer"},
	}
	for _, c := range cases {
		o := parseOrigin([]byte(c.raw))
		if c.wantNil {
			if o != nil {
				t.Errorf("parseOrigin(%q) = %+v, want nil", c.raw, o)
			}
			continue
		}
		if o == nil || o.Kind != c.wantKind || o.Subkind != c.wantSub || string(o.Raw) != c.raw {
			t.Errorf("parseOrigin(%q) = %+v", c.raw, o)
		}
	}
	var nilOrigin *Origin
	if nilOrigin.IsTaskNotification() {
		t.Error("nil Origin reports task-notification")
	}
}

func TestParseUserEventOrigin(t *testing.T) {
	events := parseLines(t,
		`{"type":"user","message":{"role":"user","content":"<task-notification>\n<task-id>b1</task-id>\n<status>completed</status>\n</task-notification>"},"session_id":"s","parent_tool_use_id":null,"uuid":"u1","isReplay":true,"origin":{"kind":"task-notification"}}`,
		`{"type":"user","message":{"role":"user","content":"hello"},"session_id":"s","parent_tool_use_id":null,"uuid":"u2","isReplay":true}`,
	)
	var users []*UserEvent
	for _, ev := range events {
		if u, ok := ev.(*UserEvent); ok {
			users = append(users, u)
		}
	}
	if len(users) != 2 {
		t.Fatalf("got %d user events, want 2", len(users))
	}
	if !users[0].Origin.IsTaskNotification() {
		t.Errorf("notification echo Origin = %+v", users[0].Origin)
	}
	if users[1].Origin != nil {
		t.Errorf("prompt echo Origin = %+v, want nil", users[1].Origin)
	}
}

// RunText on a --resume that first closes orphaned tasks returned the empty
// notification result instead of the answer.
func TestRunTextSkipsTaskNotificationResult(t *testing.T) {
	// One-shot runs have no replay echo.
	lines := withoutReplays(fixtureLines(t, "task_notification_resume.jsonl"))
	client := NewWithExecutor(NewFixtureExecutor(strings.NewReader(strings.Join(lines, "\n") + "\n")))
	text, result, err := client.RunText(context.Background(), "Reply with just the word HI.")
	if err != nil {
		t.Fatal(err)
	}
	if text != "HI" || result.Unsolicited {
		t.Errorf("RunText = %q (Unsolicited=%v), want HI", text, result.Unsolicited)
	}
}

func TestRunBlockingSkipsTaskNotificationResult(t *testing.T) {
	fixture := `[
{"type":"system","subtype":"init","session_id":"s"},
{"type":"result","subtype":"success","result":"","num_turns":0,"session_id":"s","origin":{"kind":"task-notification"},"result_index":0},
{"type":"result","subtype":"success","result":"HI","num_turns":1,"session_id":"s","result_index":1}
]`
	client := NewWithExecutor(&staticExecutor{stdout: []byte(fixture)})
	result, err := client.RunBlocking(context.Background(), "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "HI" {
		t.Errorf("text = %q, want HI", result.Text)
	}
}

func TestSessionArgsAlwaysReplay(t *testing.T) {
	for _, opts := range [][]Option{nil, {WithReplayUserMessages()}} {
		args := resolveOptions(nil, opts).buildSessionArgs()
		if n := strings.Count(strings.Join(args, " "), "--replay-user-messages"); n != 1 {
			t.Errorf("opts %d: --replay-user-messages appears %d times in %v", len(opts), n, args)
		}
	}
}

// nextResult reads events until a ResultEvent, failing after d.
func nextResult(t *testing.T, ch <-chan Event, d time.Duration) *ResultEvent {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("events closed before a ResultEvent")
			}
			if r, isResult := ev.(*ResultEvent); isResult {
				return r
			}
		case <-deadline:
			t.Fatal("timeout waiting for a ResultEvent")
		}
	}
}

func waitReturned(s *Session) <-chan *ResultEvent {
	ch := make(chan *ResultEvent, 1)
	go func() {
		r, _ := s.Wait()
		ch <- r
	}()
	return ch
}

// Resume after orphaned tasks: the empty notification result arrives while
// the query is pending, before its prompt was echoed. It must not answer the
// query, end the turn, or flip the session to Idle.
func TestSessionResumeTaskNotificationResult(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replays bool // WithReplayUserMessages
		strip   bool // fixture without echo lines (echo never seen)
	}{
		{name: "echo hidden"},
		{name: "echo surfaced", replays: true},
		{name: "no echo", strip: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := fixtureLines(t, "task_notification_resume.jsonl")
			if tc.strip {
				lines = withoutReplays(lines)
			}
			head, tail := splitAfterResults(t, lines, 1)

			sim := newSessionSim()
			var opts []Option
			if tc.replays {
				opts = append(opts, WithReplayUserMessages())
			}
			client := NewWithExecutor(sim.bidi, opts...)
			phantomSent := make(chan struct{})
			release := make(chan struct{})
			go func() {
				sim.handleInit(t)
				sim.readStdin(t) // the query
				for _, l := range head {
					sim.send(l)
				}
				close(phantomSent)
				<-release
				for _, l := range tail {
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
			waited := waitReturned(session)

			phantom := nextResult(t, session.Events(), 2*time.Second)
			<-phantomSent
			if !phantom.Unsolicited || !phantom.Origin.IsTaskNotification() {
				t.Fatalf("first result = %+v, want the unsolicited notification result", phantom)
			}
			select {
			case r := <-waited:
				t.Fatalf("Wait returned %+v on the notification result", r)
			case <-time.After(50 * time.Millisecond):
			}
			if st := session.State(); st != StateRunning {
				t.Errorf("State after notification result = %s, want running", st)
			}
			if a := session.ActivityState(); a == ActivityIdle {
				t.Errorf("ActivityState after notification result = %s, want non-idle", a)
			}

			close(release)
			var sawEcho bool
			var answer *ResultEvent
			for answer == nil {
				select {
				case ev := <-session.Events():
					switch e := ev.(type) {
					case *UserEvent:
						if e.IsReplay {
							sawEcho = true
						}
					case *ResultEvent:
						answer = e
					}
				case <-time.After(2 * time.Second):
					t.Fatal("timeout waiting for the answer")
				}
			}
			if answer.Unsolicited || answer.Text != "HI" {
				t.Errorf("answer = %+v", answer)
			}
			if want := tc.replays && !tc.strip; sawEcho != want {
				t.Errorf("replay echo on Events() = %v, want %v", sawEcho, want)
			}
			select {
			case r := <-waited:
				if r == nil || r.Text != "HI" {
					t.Errorf("Wait = %+v, want HI", r)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Wait did not return after the answer")
			}
			if st := session.State(); st != StateIdle {
				t.Errorf("State after answer = %s, want idle", st)
			}
		})
	}
}

// Same sequence through QueryCtx: the notification result goes to the orphan
// mailbox with no generation, the handle gets the answer.
func TestQueryCtxResumeTaskNotificationResult(t *testing.T) {
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

	h, err := session.QueryCtx(context.Background(), "Reply with just the word HI.")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := h.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "HI" || r.Unsolicited {
		t.Errorf("handle result = %+v, want HI", r)
	}

	var orphaned []*ResultEvent
	for _, oe := range session.DrainOrphans() {
		if res, ok := oe.Event.(*ResultEvent); ok {
			if oe.ActiveQueryAtArrival != 0 {
				t.Errorf("orphaned result stamped with generation %d, want 0", oe.ActiveQueryAtArrival)
			}
			orphaned = append(orphaned, res)
		}
	}
	if len(orphaned) != 1 || !orphaned[0].Unsolicited {
		t.Errorf("orphaned results = %+v, want the one notification result", orphaned)
	}
}

// Wake-up after end_turn: with no query pending, the notification turn's
// result must not replace the result Wait returns for the finished query.
func TestSessionWakeResultDoesNotReplaceQueryResult(t *testing.T) {
	lines := fixtureLines(t, "task_notification_wake.jsonl")
	first, wake := splitAfterResults(t, lines, 1)

	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)
	go func() {
		sim.handleInit(t)
		sim.readStdin(t)
		for _, l := range first {
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

	if err := session.Query("start"); err != nil {
		t.Fatal(err)
	}
	r1 := nextResult(t, session.Events(), 2*time.Second)
	if r1.Unsolicited || r1.Text != "STARTED" {
		t.Fatalf("first result = %+v", r1)
	}
	r2 := nextResult(t, session.Events(), 2*time.Second)
	if !r2.Unsolicited || r2.NumTurns != 1 || r2.Text == "" {
		t.Fatalf("wake result = %+v, want an unsolicited result with model output", r2)
	}
	waitCond(t, time.Second, "activity idle after wake turn", func() bool {
		return session.ActivityState() == ActivityIdle
	})
	// Wait was not called before the wake result arrived.
	got, err := session.Wait()
	if err != nil || got != r1 {
		t.Errorf("Wait = %+v, %v; want the STARTED result", got, err)
	}
	if st := session.State(); st != StateIdle {
		t.Errorf("State = %s, want idle", st)
	}
}

// A query sent while the wake-up turn is running: the wake result arrives
// first and must not answer it.
func TestSessionQueryDuringWakeTurn(t *testing.T) {
	lines := fixtureLines(t, "task_notification_wake.jsonl")
	first, wake := splitAfterResults(t, lines, 1)

	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi, WithReplayUserMessages())
	go func() {
		sim.handleInit(t)
		sim.readStdin(t)
		for _, l := range first {
			sim.send(l)
		}
		sim.readStdin(t) // second query, sent before the wake turn ends
		for _, l := range wake {
			sim.send(l)
		}
		sim.send(`{"type":"user","message":{"role":"user","content":"Reply with just PONG."},"session_id":"s","parent_tool_use_id":null,"isReplay":true}`)
		sim.sendAnswer("PONG")
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	defer sim.bidi.StdoutWriter.Close()

	if err := session.Query("start"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := session.Query("Reply with just PONG."); err != nil {
		t.Fatal(err)
	}
	got, err := session.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if got.Unsolicited || got.Text != "PONG" {
		t.Errorf("Wait = %+v, want PONG", got)
	}
}

// Fold: the prompt's echo arrives inside the notification turn, so that
// turn's task-notification result is the answer. Treating it as unsolicited
// would block Wait forever — the CLI sends no further result.
func TestSessionFoldedPromptTaskNotificationResult(t *testing.T) {
	for _, routed := range []bool{false, true} {
		name := "Wait"
		if routed {
			name = "QueryCtx"
		}
		t.Run(name, func(t *testing.T) {
			lines := fixtureLines(t, "task_notification_fold.jsonl")
			first, rest := splitAfterResults(t, lines, 1)

			sim := newSessionSim()
			client := NewWithExecutor(sim.bidi)
			go func() {
				sim.handleInit(t)
				sim.readStdin(t)
				for _, l := range first {
					sim.send(l)
				}
				sim.readStdin(t) // "PONG", folded into the notification turn
				for _, l := range rest {
					sim.send(l)
				}
			}()

			session, err := client.Connect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			defer sim.bidi.StdoutWriter.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			var got *ResultEvent
			if routed {
				h, err := session.QueryCtx(ctx, "start")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := h.Wait(ctx); err != nil {
					t.Fatal(err)
				}
				h2, err := session.QueryCtx(ctx, "Reply with just the word PONG.")
				if err != nil {
					t.Fatal(err)
				}
				if got, err = h2.Wait(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := session.Query("start"); err != nil {
					t.Fatal(err)
				}
				if _, err := session.Wait(); err != nil {
					t.Fatal(err)
				}
				if err := session.Query("Reply with just the word PONG."); err != nil {
					t.Fatal(err)
				}
				done := make(chan struct{})
				go func() {
					got, _ = session.Wait()
					close(done)
				}()
				select {
				case <-done:
				case <-ctx.Done():
					t.Fatal("Wait blocked on a folded prompt")
				}
			}
			if got == nil || got.Unsolicited || got.Text != "PONG" || !got.Origin.IsTaskNotification() {
				t.Errorf("answer = %+v, want the task-notification result with PONG", got)
			}
			if st := session.State(); st != StateIdle {
				t.Errorf("State = %s, want idle", st)
			}
		})
	}
}

// The echo of a task notification folded into the query's own turn carries
// origin task-notification; it is not the prompt's echo and must not make a
// later notification result count as the answer.
func TestSessionNotificationEchoIsNotPromptEcho(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)
	go func() {
		sim.handleInit(t)
		sim.readStdin(t)
		sim.send(`{"type":"user","message":{"role":"user","content":"<task-notification>x</task-notification>"},"session_id":"s","parent_tool_use_id":null,"isReplay":true,"origin":{"kind":"task-notification"}}`)
		sim.send(`{"type":"result","subtype":"success","num_turns":0,"result":"","session_id":"s","origin":{"kind":"task-notification"},"usage":{"input_tokens":0,"output_tokens":0}}`)
		sim.send(`{"type":"user","message":{"role":"user","content":"q"},"session_id":"s","parent_tool_use_id":null,"isReplay":true}`)
		sim.sendAnswer("A")
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	defer sim.bidi.StdoutWriter.Close()

	if err := session.Query("q"); err != nil {
		t.Fatal(err)
	}
	got, err := session.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "A" {
		t.Errorf("Wait = %+v, want A", got)
	}
}
