//go:build integration

package claudecli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestIntegrationProjectsDirWithEnv checks ProjectsDir against where a real
// CLI writes its session transcript. The fresh config dir has no credentials,
// so the run fails to authenticate; the CLI still records the prompt first.
func TestIntegrationProjectsDirWithEnv(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	account := t.TempDir()
	sessionID := newTestSessionID()
	client := New(
		WithModel(ModelHaiku),
		WithWorkDir(t.TempDir()),
		WithEnv(map[string]string{"CLAUDE_CONFIG_DIR": account}),
	)
	root, err := client.ProjectsDir()
	if err != nil {
		t.Fatal(err)
	}
	_, _, runErr := client.RunText(ctx, "Say hi", WithSessionID(sessionID))
	t.Logf("run error (expected without credentials): %v", runErr)

	matches, _ := filepath.Glob(filepath.Join(root, "*", sessionID+".jsonl"))
	if len(matches) != 1 {
		t.Fatalf("transcript for %s under %s: %v", sessionID, root, matches)
	}
}

// TestIntegrationEmptyConfigDirIsCwdRelative pins why ProjectsDir refuses an
// empty CLAUDE_CONFIG_DIR: the CLI writes projects/ under its working dir.
func TestIntegrationEmptyConfigDirIsCwdRelative(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	work := t.TempDir()
	client := New(
		WithModel(ModelHaiku),
		WithWorkDir(work),
		WithEnv(map[string]string{"CLAUDE_CONFIG_DIR": ""}),
	)
	if _, err := client.ProjectsDir(); !errors.Is(err, ErrConfigDirNotAbsolute) {
		t.Fatalf("ProjectsDir err = %v, want ErrConfigDirNotAbsolute", err)
	}
	_, _, runErr := client.RunText(ctx, "Say hi", WithSessionID(newTestSessionID()))
	t.Logf("run error: %v", runErr)
	if _, err := os.Stat(filepath.Join(work, "projects")); err != nil {
		t.Errorf("projects/ under the working dir: %v", err)
	}
}

// TestIntegrationAuthStatusWithEnv checks that AuthStatus queries the account
// the client's CLAUDE_CONFIG_DIR names. A fresh config dir holds no
// credentials, so it must not report the process's logged-in account.
func TestIntegrationAuthStatusWithEnv(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	process, err := New().AuthStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if process.Status != AuthStateAuthenticated {
		t.Skipf("process account is %s; need a logged-in one to tell them apart", process.Status)
	}
	fresh, err := New(WithEnv(map[string]string{"CLAUDE_CONFIG_DIR": t.TempDir()})).AuthStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != AuthStateUnauthenticated {
		t.Errorf("fresh config dir: Status = %s (email %q), want unauthenticated", fresh.Status, fresh.Email)
	}
}
