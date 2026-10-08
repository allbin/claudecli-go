package claudecli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

// LoginError is the CLI's verdict that a submitted code did not sign in.
//
// Retryable reports whether the same LoginProcess still accepts a code. The
// CLI keeps waiting after input that is not CODE#STATE, but exits after a
// well-formed code fails the token exchange (wrong, expired or already used),
// so that case needs a new AuthLogin.
type LoginError struct {
	// Message is the CLI's text after "Login failed: " (e.g. "Request failed
	// with status code 400"), or its "Invalid code" line. Input rejected
	// before it reaches the CLI carries this package's own text.
	Message   string
	Retryable bool
}

func (e *LoginError) Error() string { return "login failed: " + e.Message }

var (
	errLoginCancelled = errors.New("auth login: cancelled")
	errLoginFinished  = errors.New("auth login: login already finished")
)

// SubmitManualCode completes the login with the CODE#STATE that Claude's page
// shows after the person authorizes URL. It writes the code to the CLI's
// prompt and blocks until the CLI reports the outcome:
//   - nil: the CLI printed "Login successful." and has stored the credentials.
//   - *LoginError: the CLI rejected the code; see LoginError.Retryable.
//   - ctx.Err(): no verdict before ctx ended. The CLI may still be exchanging
//     the code; Wait for it or Cancel and start again.
//
// Codes for AutoOpenURL (redirect_uri=localhost) do not work here; use
// SubmitCode, or Submit, which routes either kind.
func (p *LoginProcess) SubmitManualCode(ctx context.Context, code string) error {
	code = strings.TrimSpace(code)
	if err := validateManualCode(code); err != nil {
		return err
	}
	if p.stdin == nil || p.outcome == nil {
		return errors.New("auth login: no CLI prompt to submit to")
	}
	p.submitMu.Lock()
	defer p.submitMu.Unlock()
	seen, err := p.outcome.begin()
	if err != nil {
		return err
	}
	p.log().Debug("submit manual code")
	if _, err := io.WriteString(p.stdin, code+"\n"); err != nil {
		return fmt.Errorf("auth login: write code: %w", err)
	}
	return p.outcome.await(ctx, seen)
}

// Submit completes the login with whatever the person pasted, and blocks
// until the CLI reports the outcome, as SubmitManualCode does:
//   - the address of the failed localhost page (http://localhost:PORT/callback?…)
//     goes to the CLI's callback server, as SubmitCode does;
//   - the address of Claude's code page (…/oauth/code/callback?code=…&state=…)
//     and anything else go to the CLI's prompt as CODE#STATE.
func (p *LoginProcess) Submit(ctx context.Context, input string) error {
	input = strings.TrimSpace(input)
	if !isLocalhostURL(input) {
		if code, ok := manualCodeFromURL(input); ok {
			input = code
		}
		return p.SubmitManualCode(ctx, input)
	}
	if p.outcome == nil {
		return errors.New("auth login: no CLI to submit to")
	}
	p.submitMu.Lock()
	defer p.submitMu.Unlock()
	seen, err := p.outcome.begin()
	if err != nil {
		return err
	}
	if err := p.submitCallback(ctx, input); err != nil {
		return err
	}
	return p.outcome.await(ctx, seen)
}

func isLocalhostURL(s string) bool {
	return strings.HasPrefix(s, "http://localhost") || strings.HasPrefix(s, "https://localhost")
}

// manualCodeFromURL turns the address of Claude's code page, where the manual
// flow's redirect lands, into the CODE#STATE the page displays.
func manualCodeFromURL(s string) (string, bool) {
	if !strings.HasPrefix(s, "https://") {
		return "", false
	}
	u, err := url.Parse(s)
	if err != nil || !strings.HasSuffix(u.Path, "/oauth/code/callback") {
		return "", false
	}
	code, state := u.Query().Get("code"), u.Query().Get("state")
	if code == "" || state == "" {
		return "", false
	}
	return code + "#" + state, true
}

// validateManualCode applies the CLI's own check (two non-empty parts around
// '#') before the code is written, plus one line of no whitespace, since a
// newline would split it into two submissions.
func validateManualCode(code string) error {
	c, s, ok := strings.Cut(code, "#")
	if !ok || c == "" || s == "" || strings.ContainsAny(code, " \t\r\n") {
		return &LoginError{
			Message:   "invalid code: expected CODE#STATE as shown on Claude's page",
			Retryable: true,
		}
	}
	return nil
}

// loginOutcome tracks what the login CLI has reported on its output.
type loginOutcome struct {
	mu         sync.Mutex
	changed    chan struct{} // closed and replaced on every report
	invalid    int           // "Invalid code" reports so far
	invalidMsg string
	finished   bool
	err        error // the final verdict once finished; nil on success
}

func newLoginOutcome() *loginOutcome {
	return &loginOutcome{changed: make(chan struct{})}
}

func (o *loginOutcome) notifyLocked() {
	close(o.changed)
	o.changed = make(chan struct{})
}

// finish records the final verdict. The first one wins.
func (o *loginOutcome) finish(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.finished {
		return
	}
	o.finished = true
	o.err = err
	o.notifyLocked()
}

// exit records the CLI's exit as the verdict, when its output gave none.
func (o *loginOutcome) exit(err error) {
	if err != nil {
		err = fmt.Errorf("auth login: CLI exited without reporting an outcome: %w", err)
	}
	o.finish(err)
}

// observe classifies one ANSI-stripped output line. The prompt has no
// newline, so a verdict can share a line with it.
func (o *loginOutcome) observe(line string) {
	switch {
	case strings.Contains(line, "Login successful"):
		o.finish(nil)
	case strings.Contains(line, "Login failed"):
		_, msg, _ := strings.Cut(line, "Login failed")
		msg = strings.TrimSpace(strings.TrimLeft(msg, ":."))
		o.finish(&LoginError{Message: msg})
	case strings.Contains(line, "Invalid code"):
		i := strings.Index(line, "Invalid code")
		o.mu.Lock()
		o.invalid++
		o.invalidMsg = strings.TrimSpace(line[i:])
		o.notifyLocked()
		o.mu.Unlock()
	}
}

// begin returns the point a submission's verdict is counted from, or an
// error when the login has already finished.
func (o *loginOutcome) begin() (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.finished {
		if o.err != nil {
			return 0, fmt.Errorf("%w: %w", errLoginFinished, o.err)
		}
		return 0, errLoginFinished
	}
	return o.invalid, nil
}

// await blocks until the CLI reports a verdict after the point begin returned.
func (o *loginOutcome) await(ctx context.Context, seen int) error {
	for {
		o.mu.Lock()
		if o.finished {
			err := o.err
			o.mu.Unlock()
			return err
		}
		if o.invalid > seen {
			msg := o.invalidMsg
			o.mu.Unlock()
			return &LoginError{Message: msg, Retryable: true}
		}
		ch := o.changed
		o.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// ansiRe matches CSI and OSC escape sequences.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)

// stripANSI removes terminal escapes and a trailing CR, in case the CLI
// colours its output (e.g. FORCE_COLOR in the host environment).
func stripANSI(s string) string {
	return strings.TrimRight(ansiRe.ReplaceAllString(s, ""), "\r")
}
