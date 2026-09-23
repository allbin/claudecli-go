//go:build integration

package claudecli

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// eventLog drains a Session's Events() in the background (an unread channel
// stalls the pump) and lets the test wait for a condition on what arrived.
type eventLog struct {
	mu     sync.Mutex
	events []Event
	done   chan struct{}
}

func logEvents(s *Session) *eventLog {
	l := &eventLog{done: make(chan struct{})}
	go func() {
		defer close(l.done)
		for ev := range s.Events() {
			l.mu.Lock()
			l.events = append(l.events, ev)
			l.mu.Unlock()
		}
	}()
	return l
}

func (l *eventLog) results() []*ResultEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return results(l.events)
}

func (l *eventLog) waitFor(t *testing.T, d time.Duration, what string, cond func([]*ResultEvent) bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond(l.results()) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout after %s waiting for %s; results: %+v", d, what, l.results())
}

func taskNotificationClient(dir string) *Client {
	return New(
		WithModel(ModelHaiku),
		WithWorkDir(dir),
		WithTools("Bash"),
		WithPermissionMode(PermissionBypass),
	)
}

const startBackgroundSleep = "Use the Bash tool with run_in_background set to true to run exactly `sleep %s`. " +
	"Do not wait for it or check on it. Then reply with just the word STARTED."

// TestIntegrationResumeOrphanedTaskNotification kills a session while a
// background shell is still running, resumes it, and checks that the empty
// task-notification result the CLI emits first does not answer the query.
func TestIntegrationResumeOrphanedTaskNotification(t *testing.T) {
	dir := t.TempDir()
	client := taskNotificationClient(dir)

	ctx1, kill := context.WithCancel(context.Background())
	defer kill()
	s1, err := client.Connect(ctx1)
	if err != nil {
		t.Fatal(err)
	}
	logEvents(s1)
	if err := s1.Query(strings.Replace(startBackgroundSleep, "%s", "180", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s1.Wait(); err != nil {
		t.Fatal(err)
	}
	sid := s1.SessionID()
	kill() // SIGTERM while the background sleep is alive
	s1.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s2, err := client.Connect(ctx, WithResume(sid))
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	log := logEvents(s2)
	if err := s2.Query("Reply with just the word HI."); err != nil {
		t.Fatal(err)
	}
	got, err := s2.Wait()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range log.results() {
		t.Logf("result unsolicited=%v origin=%+v index=%v turns=%d text=%q", r.Unsolicited, r.Origin, derefInt(r.ResultIndex), r.NumTurns, r.Text)
	}
	if got.Unsolicited || !strings.Contains(got.Text, "HI") {
		t.Errorf("Wait = %+v, want the HI answer", got)
	}
	var sawUnsolicited bool
	for _, r := range log.results() {
		sawUnsolicited = sawUnsolicited || (r.Unsolicited && r.Origin.IsTaskNotification())
	}
	if !sawUnsolicited {
		t.Error("no unsolicited task-notification result on resume: the CLI changed, re-check the phantom-result handling")
	}
}

// TestIntegrationWakeAfterBackgroundTask checks the result the CLI emits by
// itself when a background task finishes after end_turn: it is unsolicited
// and does not replace the query's result.
func TestIntegrationWakeAfterBackgroundTask(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s, err := taskNotificationClient(t.TempDir()).Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	log := logEvents(s)

	if err := s.Query(strings.Replace(startBackgroundSleep, "%s", "10", 1)); err != nil {
		t.Fatal(err)
	}
	first, err := s.Wait()
	if err != nil {
		t.Fatal(err)
	}
	log.waitFor(t, 90*time.Second, "the wake-up result", func(rs []*ResultEvent) bool {
		return len(rs) >= 2
	})
	wake := log.results()[1]
	t.Logf("wake result unsolicited=%v origin=%+v turns=%d text=%q", wake.Unsolicited, wake.Origin, wake.NumTurns, wake.Text)
	if !wake.Unsolicited || !wake.Origin.IsTaskNotification() {
		t.Errorf("wake result = %+v, want unsolicited task-notification", wake)
	}
	if again, _ := s.Wait(); again != first {
		t.Errorf("Wait after wake = %+v, want the STARTED result", again)
	}
	if st := s.State(); st != StateIdle {
		t.Errorf("State = %s, want idle", st)
	}
}

// TestIntegrationPromptDuringWakeTurn sends a prompt while the wake-up turn
// runs a foreground tool. When the CLI folds the prompt into that turn, the
// turn's task-notification result is its only answer; either way Wait must
// return.
func TestIntegrationPromptDuringWakeTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	s, err := taskNotificationClient(t.TempDir()).Connect(ctx, WithReplayUserMessages())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	log := logEvents(s)

	if err := s.Query("Use the Bash tool with run_in_background set to true to run exactly `sleep 10`. " +
		"Do not wait for it. Reply with just STARTED. IMPORTANT: later, when you are notified that the " +
		"background command completed, you must use the Bash tool (foreground, not background) to run " +
		"`sleep 12` before replying, and then reply with just NOTIFIED."); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Wait(); err != nil {
		t.Fatal(err)
	}
	// Wait for the wake-up turn to start its foreground tool.
	deadline := time.Now().Add(90 * time.Second)
	for s.ActivityState() != ActivityAwaitingToolResult {
		if time.Now().After(deadline) {
			t.Fatal("wake-up turn never ran a tool")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := s.Query("Reply with just the word PONG."); err != nil {
		t.Fatal(err)
	}
	done := make(chan *ResultEvent, 1)
	go func() {
		r, _ := s.Wait()
		done <- r
	}()
	select {
	case got := <-done:
		t.Logf("answer unsolicited=%v origin=%+v text=%q", got.Unsolicited, got.Origin, got.Text)
		if got.Unsolicited {
			t.Errorf("Wait returned an unsolicited result: %+v", got)
		}
		if got.Origin.IsTaskNotification() {
			t.Log("prompt was folded into the notification turn")
		}
	case <-ctx.Done():
		t.Fatalf("Wait blocked; results: %+v", log.results())
	}
}

func derefInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
