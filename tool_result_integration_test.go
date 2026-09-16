//go:build integration

package claudecli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestIntegrationToolResultIsError runs a real failing Read (a missing file)
// next to a real successful one and checks the flag reaches UserContent.
func TestIntegrationToolResultIsError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "present.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	client := New(
		WithModel(ModelHaiku),
		WithWorkDir(dir),
		WithTools("Read"),
		WithPermissionMode(PermissionBypass),
		WithMaxTurns(6),
	)
	stream := client.Run(ctx, "Use the Read tool twice, in this order: first on "+
		filepath.Join(dir, "missing.txt")+", then on "+filepath.Join(dir, "present.txt")+
		". Then reply DONE.")

	var results []UserContent
	for ev := range stream.Events() {
		if ue, ok := ev.(*UserEvent); ok {
			for _, c := range ue.Content {
				if c.Type == "tool_result" {
					results = append(results, c)
				}
			}
		}
	}
	if _, err := stream.Wait(); err != nil {
		t.Fatalf("run: %v", err)
	}
	var failed, succeeded int
	for _, r := range results {
		t.Logf("tool_result %s IsError=%v text=%.80q", r.ToolUseID, r.IsError, contentText(r.Content))
		if r.IsError {
			failed++
		} else {
			succeeded++
		}
	}
	if failed == 0 || succeeded == 0 {
		t.Errorf("want at least one flagged and one unflagged result, got %d flagged, %d unflagged", failed, succeeded)
	}
}

func contentText(cs []ToolContent) string {
	var s string
	for _, c := range cs {
		s += c.Text
	}
	return s
}
