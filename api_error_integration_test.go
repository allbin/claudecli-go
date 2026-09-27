//go:build integration

package claudecli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// oversizedPrompt is ~1.6 MB of text: past any Haiku context window, so the
// CLI rejects it before the model runs.
func oversizedPrompt() string {
	var b strings.Builder
	for i := 0; i < 150000; i++ {
		fmt.Fprintf(&b, "word%d ", i)
	}
	return b.String()
}

func TestIntegrationPromptTooLongRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client := New(WithModel(ModelHaiku), WithStrictMCPConfig())
	stream := client.Run(ctx, oversizedPrompt())
	var last *ErrorEvent
	for ev := range stream.Events() {
		switch e := ev.(type) {
		case *TextEvent:
			t.Errorf("error text leaked as TextEvent: %q", e.Content)
		case *ErrorEvent:
			if e.Fatal {
				last = e
			}
		}
	}
	_, err := stream.Wait()
	if !errors.Is(err, ErrContextWindowExceeded) {
		t.Fatalf("Wait = %v, want ErrContextWindowExceeded", err)
	}
	if last == nil || !errors.Is(last.Err, ErrContextWindowExceeded) {
		t.Fatalf("final fatal ErrorEvent = %v, want ErrContextWindowExceeded", last)
	}
	var cliErr *Error
	if !errors.As(last.Err, &cliErr) {
		t.Fatalf("final fatal ErrorEvent = %T, want the process-exit *Error", last.Err)
	}
	t.Logf("exit error: %v", cliErr)
}

func TestIntegrationPromptTooLongSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client := New(WithModel(ModelHaiku), WithStrictMCPConfig())
	session, err := client.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	if err := session.Query(oversizedPrompt()); err != nil {
		t.Fatal(err)
	}
	_, err = session.Wait()
	if !errors.Is(err, ErrContextWindowExceeded) {
		t.Fatalf("Wait = %v, want ErrContextWindowExceeded", err)
	}
	t.Logf("Wait error: %v (state %v)", err, session.State())
	// The CLI stays up and closes the turn with an is_error result, which
	// returns the session to Idle. Its conversation still holds the oversized
	// prompt, so on CLI 2.1.283 a follow-up fails the same way: reconnect.
	deadline := time.Now().Add(10 * time.Second)
	for session.State() != StateIdle && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if session.State() != StateIdle {
		t.Fatalf("state = %v, want StateIdle after the CLI's result", session.State())
	}
	if err := session.Query("Reply with the single word ok."); err != nil {
		t.Fatalf("follow-up Query: %v", err)
	}
	_, err = session.Wait()
	t.Logf("follow-up Wait: %v", err)
}
