//go:build integration

package claudecli

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestIntegrationPermissionWithdrawalCancelsCallbackCtx proves the whole chain
// against a real CLI: the CLI raises a can_use_tool prompt, the turn is
// interrupted while the callback is still parked, the CLI withdraws the prompt
// with control_cancel_request, and the ctx handed to a
// WithCanUseToolRequestContext callback fires.
//
// This is the case a host cannot handle without the ctx: the answer is
// discarded either way, but only the ctx tells the host to drop its dialog.
func TestIntegrationPermissionWithdrawalCancelsCallbackCtx(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "probe-target.txt")

	entered := make(chan ToolPermissionRequest, 1)
	ctxDone := make(chan error, 1)

	var parkOnce sync.Once
	client := New(
		WithModel(ModelHaiku),
		WithPermissionMode(PermissionManual),
		WithWorkDir(dir),
		WithCanUseToolRequestContext(
			func(ctx context.Context, req ToolPermissionRequest) (*PermissionResponse, error) {
				parked := false
				parkOnce.Do(func() {
					parked = true
					entered <- req
					// A host would be showing a dialog here.
					<-ctx.Done()
					ctxDone <- ctx.Err()
				})
				if parked {
					return &PermissionResponse{Allow: true}, ctx.Err()
				}
				// Later prompts (the second query below) answer normally —
				// proof the cancel was scoped to the withdrawn request id and
				// did not poison the session.
				return &PermissionResponse{Allow: true}, nil
			}),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	session, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer session.Close()

	turnEnded := make(chan struct{}, 4)
	go func() {
		for ev := range session.Events() {
			if _, ok := ev.(*ResultEvent); ok {
				select {
				case turnEnded <- struct{}{}:
				default:
				}
			}
		}
	}()

	if err := session.Query("Write the single word hello to probe-target.txt using the Write tool. Do not ask me anything first."); err != nil {
		t.Fatalf("Query: %v", err)
	}

	var req ToolPermissionRequest
	select {
	case req = <-entered:
		t.Logf("prompt: tool=%s tool_use_id=%s reason=%q/%s",
			req.ToolName, req.ToolUseID, req.DecisionReason, req.DecisionReasonType)
	case <-time.After(90 * time.Second):
		t.Fatal("CLI never raised a can_use_tool prompt")
	}

	// Interrupting the turn is what makes the CLI withdraw the pending prompt.
	if err := session.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	select {
	case cerr := <-ctxDone:
		t.Logf("callback ctx cancelled: %v", cerr)
	case <-time.After(30 * time.Second):
		t.Fatal("callback ctx never cancelled after the CLI withdrew the prompt")
	}

	// The withdrawn answer must not have been applied: we returned Allow.
	if _, err := os.Stat(target); err == nil {
		t.Errorf("%s was written — the discarded allow reached the CLI", target)
	}

	// A per-request cancel is not a session teardown. The interrupted turn
	// must end normally and the session must still round-trip control
	// requests afterwards.
	select {
	case <-turnEnded:
	case <-time.After(60 * time.Second):
		t.Fatal("interrupted turn never produced a result")
	}
	if err := session.Ping(30 * time.Second); err != nil {
		t.Fatalf("session dead after withdrawal: %v", err)
	}

	if err := session.Query("Reply with just the word ok."); err != nil {
		t.Fatalf("Query after withdrawal: %v", err)
	}
	select {
	case <-turnEnded:
	case <-time.After(60 * time.Second):
		t.Fatal("second turn never completed")
	}
}
