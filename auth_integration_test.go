//go:build integration

package claudecli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestIntegrationAuthLoginManualCode drives a real `claude auth login` in a
// fresh config dir up to the manual-code prompt: the CLI's own "Invalid
// code" line leaves the process waiting, and a well-formed bogus code ends
// it with the CLI's "Login failed" text. The user's ~/.claude is never used.
func TestIntegrationAuthLoginManualCode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	account := t.TempDir()
	client := New(WithEnv(map[string]string{"CLAUDE_CONFIG_DIR": account}))
	p, err := client.AuthLogin(ctx, WithAuthMethod(AuthMethodClaudeAI), WithNoBrowser())
	if err != nil {
		t.Fatalf("AuthLogin: %v", err)
	}
	if p == nil {
		t.Fatal("AuthLogin returned no process: the temp config dir is somehow logged in")
	}
	defer func() { _ = p.Cancel() }()

	if !strings.Contains(p.URL, "redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback") {
		t.Errorf("URL = %q, want the manual platform.claude.com redirect", p.URL)
	}
	if p.CallbackPort() == 0 {
		t.Errorf("CallbackPort = 0, AutoOpenURL = %q", p.AutoOpenURL)
	}

	// Bypass our own format check to reach the CLI's.
	seen, err := p.outcome.begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.stdin.Write([]byte("nohash\n")); err != nil {
		t.Fatal(err)
	}
	err = p.outcome.await(ctx, seen)
	var le *LoginError
	if !errors.As(err, &le) || !le.Retryable {
		t.Fatalf("malformed code: err = %v, want retryable *LoginError", err)
	}
	t.Logf("malformed code: %v", err)

	err = p.SubmitManualCode(ctx, "bogusCODE#bogusSTATE")
	if !errors.As(err, &le) || le.Retryable {
		t.Fatalf("bogus code: err = %v, want final *LoginError", err)
	}
	t.Logf("bogus code: %v", err)
	if !strings.Contains(le.Message, "400") {
		t.Errorf("Message = %q, want the token exchange's 400", le.Message)
	}
	if err := p.Wait(); err == nil {
		t.Error("Wait = nil after a bogus code, want the CLI's exit error")
	}
	if _, err := os.Stat(filepath.Join(account, ".credentials.json")); err == nil {
		t.Error("credentials written after a bogus code")
	}
}
