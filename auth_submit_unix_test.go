//go:build !windows

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

// fakeLoginCLI mimics `claude auth login` as of 2.1.292: it opens $BROWSER
// with the localhost URL, prints the manual URL and an unterminated prompt,
// then reads codes from stdin. The verdicts go to the same streams as the
// real CLI's: success on stdout, failures on stderr.
const fakeLoginCLI = `#!/bin/sh
"$BROWSER" "https://claude.com/cai/oauth/authorize?code=true&redirect_uri=http%3A%2F%2Flocalhost%3A54545%2Fcallback&state=S"
echo "Opening browser to sign in…"
echo "If the browser didn't open, visit: https://claude.com/cai/oauth/authorize?code=true&redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback&state=S"
printf 'Paste code here if prompted > '
while IFS= read -r line; do
  case "$line" in
    *'#'*) ;;
    *) echo "Invalid code. Please make sure the full code was copied." >&2; continue ;;
  esac
  case "$line" in
    goodCODE#S) echo "Login successful."; exit 0 ;;
    colorCODE#S) printf '\033[31mLogin failed: Request failed with status code 400\033[39m\n' >&2; exit 1 ;;
    hang*) sleep 30; exit 1 ;;
    *) echo "Login failed: Request failed with status code 400" >&2; exit 1 ;;
  esac
done
exit 1
`

func startFakeLogin(t *testing.T) *LoginProcess {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bin, []byte(fakeLoginCLI), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	p, err := New(WithBinaryPath(bin)).AuthLogin(ctx, WithNoBrowser())
	if err != nil {
		t.Fatalf("AuthLogin: %v", err)
	}
	if p == nil {
		t.Fatal("AuthLogin returned no process")
	}
	t.Cleanup(func() { _ = p.Cancel(); _ = p.Wait() })
	return p
}

func TestAuthLoginFakeCLIURLs(t *testing.T) {
	p := startFakeLogin(t)
	if !strings.Contains(p.URL, "platform.claude.com") {
		t.Errorf("URL = %q, want the manual platform.claude.com redirect", p.URL)
	}
	if p.CallbackPort() != 54545 {
		t.Errorf("CallbackPort = %d, want 54545 from the BROWSER capture", p.CallbackPort())
	}
}

func TestSubmitManualCodeSuccess(t *testing.T) {
	p := startFakeLogin(t)
	if err := p.SubmitManualCode(context.Background(), " goodCODE#S\n"); err != nil {
		t.Fatalf("SubmitManualCode: %v", err)
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if err := p.SubmitManualCode(context.Background(), "goodCODE#S"); !errors.Is(err, errLoginFinished) {
		t.Errorf("second submit = %v, want errLoginFinished", err)
	}
}

func TestSubmitManualCodeFailure(t *testing.T) {
	for _, code := range []string{"bogusCODE#S", "colorCODE#S"} {
		t.Run(code, func(t *testing.T) {
			p := startFakeLogin(t)
			err := p.SubmitManualCode(context.Background(), code)
			var le *LoginError
			if !errors.As(err, &le) {
				t.Fatalf("err = %v, want *LoginError", err)
			}
			if le.Message != "Request failed with status code 400" || le.Retryable {
				t.Errorf("LoginError = %+v, want the CLI's text and not retryable", le)
			}
			if err := p.Wait(); err == nil {
				t.Error("Wait = nil, want the CLI's exit error")
			}
		})
	}
}

// The CLI keeps waiting after input that is not CODE#STATE; this package
// rejects it before writing, and the process still takes a good code.
func TestSubmitManualCodeMalformedThenGood(t *testing.T) {
	p := startFakeLogin(t)
	for _, bad := range []string{"nohash", "#S", "CODE#", "a b#S"} {
		err := p.SubmitManualCode(context.Background(), bad)
		var le *LoginError
		if !errors.As(err, &le) || !le.Retryable {
			t.Fatalf("%q: err = %v, want retryable *LoginError", bad, err)
		}
	}
	if err := p.SubmitManualCode(context.Background(), "goodCODE#S"); err != nil {
		t.Fatalf("good code after malformed: %v", err)
	}
}

// The CLI's own "Invalid code" line, reached when its check disagrees with
// ours, is retryable too.
func TestSubmitManualCodeCLIInvalid(t *testing.T) {
	p := startFakeLogin(t)
	seen, err := p.outcome.begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.stdin.Write([]byte("nohash\n")); err != nil {
		t.Fatal(err)
	}
	err = p.outcome.await(context.Background(), seen)
	var le *LoginError
	if !errors.As(err, &le) || !le.Retryable || !strings.HasPrefix(le.Message, "Invalid code.") {
		t.Fatalf("err = %v, want retryable *LoginError with the CLI's text", err)
	}
	if err := p.SubmitManualCode(context.Background(), "goodCODE#S"); err != nil {
		t.Fatalf("good code after CLI rejection: %v", err)
	}
}

func TestSubmitManualCodeTimeout(t *testing.T) {
	p := startFakeLogin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := p.SubmitManualCode(ctx, "hangCODE#S"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestSubmitManualCodeCancel(t *testing.T) {
	p := startFakeLogin(t)
	errCh := make(chan error, 1)
	go func() { errCh <- p.SubmitManualCode(context.Background(), "hangCODE#S") }()
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	if err := p.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case err := <-errCh:
		if !errors.Is(err, errLoginCancelled) {
			t.Errorf("err = %v, want errLoginCancelled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SubmitManualCode did not return after Cancel")
	}
	if err := p.Wait(); err == nil {
		t.Error("Wait = nil after Cancel, want the kill's exit error")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Cancel to exit took %v", d)
	}
}

// Submit sends the address of Claude's code page to the prompt as CODE#STATE.
func TestSubmitRoutesCodePageURL(t *testing.T) {
	p := startFakeLogin(t)
	if err := p.Submit(context.Background(), "https://platform.claude.com/oauth/code/callback?code=goodCODE&state=S"); err != nil {
		t.Fatalf("Submit: %v", err)
	}
}

func TestSubmitRoutesManualCode(t *testing.T) {
	p := startFakeLogin(t)
	err := p.Submit(context.Background(), "bogusCODE#S")
	var le *LoginError
	if !errors.As(err, &le) || le.Retryable {
		t.Fatalf("err = %v, want final *LoginError", err)
	}
}
