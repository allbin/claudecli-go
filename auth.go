package claudecli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AuthState represents a three-state authentication status.
type AuthState string

const (
	AuthStateAuthenticated   AuthState = "authenticated"
	AuthStateUnauthenticated AuthState = "unauthenticated"
	AuthStateUnknown         AuthState = "unknown"
)

// AuthStatusResult represents the authentication state returned by the CLI.
type AuthStatusResult struct {
	// Status is the three-state auth status derived from defensive parsing.
	// Use this instead of LoggedIn for robust auth checks.
	Status AuthState `json:"-"`

	// Message contains a human-readable explanation when Status is not
	// AuthStateAuthenticated (e.g. "not logged in", version mismatch).
	Message string `json:"-"`

	LoggedIn         bool   `json:"loggedIn"`
	AuthMethod       string `json:"authMethod,omitempty"`  // e.g. "claude.ai", "api-key"
	APIProvider      string `json:"apiProvider,omitempty"` // e.g. "firstParty", "bedrock", "vertex"
	Email            string `json:"email,omitempty"`
	OrgID            string `json:"orgId,omitempty"`
	OrgName          string `json:"orgName,omitempty"`
	SubscriptionType string `json:"subscriptionType,omitempty"` // e.g. "team", "pro"
}

// AuthMethod selects the authentication provider for login.
type AuthMethod string

const (
	AuthMethodClaudeAI AuthMethod = "claudeai" // Claude subscription (default)
	AuthMethodConsole  AuthMethod = "console"  // Anthropic Console (API billing)
)

// authLoginConfig holds resolved login options.
type authLoginConfig struct {
	method    AuthMethod
	sso       bool
	email     string
	noBrowser bool
}

// AuthLoginOption configures AuthLogin behavior.
type AuthLoginOption func(*authLoginConfig)

// WithAuthMethod sets the authentication provider.
func WithAuthMethod(m AuthMethod) AuthLoginOption {
	return func(c *authLoginConfig) { c.method = m }
}

// WithSSO forces the SSO login flow.
func WithSSO() AuthLoginOption {
	return func(c *authLoginConfig) { c.sso = true }
}

// WithLoginEmail pre-populates the email on the login page.
func WithLoginEmail(email string) AuthLoginOption {
	return func(c *authLoginConfig) { c.email = email }
}

// WithNoBrowser suppresses the CLI's automatic browser opening by setting
// BROWSER=true in the subprocess environment. Callers that already have the
// login URL (e.g. from LoginProcess.URL) can use this to avoid duplicate tabs.
func WithNoBrowser() AuthLoginOption {
	return func(c *authLoginConfig) { c.noBrowser = true }
}

// LoginProcess represents an in-progress OAuth login.
//
// Two URLs are available, and either can complete the login:
//   - URL: the manual URL (redirect_uri=platform.claude.com). After the person
//     authorizes, Claude's page shows a CODE#STATE with a Copy button; pass it
//     to SubmitManualCode or Submit. This is the flow for signing in from a
//     different device, e.g. a phone.
//   - AutoOpenURL: the browser URL (redirect_uri=localhost:PORT). After
//     authorizing, the browser redirects to localhost, which only loads on the
//     machine running the CLI. Elsewhere the page fails to load; pass its
//     address to SubmitCode or Submit. Requires WithNoBrowser.
//
// Submit accepts whatever the person pasted from either flow and waits for
// the CLI's verdict.
type LoginProcess struct {
	URL          string // manual URL (platform.claude.com redirect, shows CODE#STATE)
	AutoOpenURL  string // browser URL (localhost redirect, may be empty)
	callbackPort int    // port of the CLI's local OAuth callback server
	tmpDir       string // temp dir for browser capture script; cleaned up on Wait/Cancel
	stdin        io.WriteCloser
	cmd          *exec.Cmd
	pp           *platformProc
	exited       chan struct{} // closed once the CLI has exited
	exitErr      error         // the CLI's exit error, valid once exited is closed
	outcome      *loginOutcome // what the CLI reported; nil only in tests
	submitMu     sync.Mutex    // one submission at a time
	logger       *slog.Logger  // may be nil
}

// CallbackPort returns the port of the CLI's local OAuth callback server.
// Returns 0 if the port could not be determined (e.g. BROWSER capture failed).
func (p *LoginProcess) CallbackPort() int { return p.callbackPort }

func (p *LoginProcess) log() *slog.Logger {
	if p.logger != nil {
		return p.logger
	}
	return slog.New(discardHandler{})
}

// Wait blocks until the login process exits. Returns nil on success, and
// otherwise the CLI's exit error. Safe to call more than once.
func (p *LoginProcess) Wait() error {
	<-p.exited
	err := p.exitErr
	if p.stdin != nil {
		p.stdin.Close()
	}
	if p.tmpDir != "" {
		os.RemoveAll(p.tmpDir)
	}
	return err
}

// SubmitCode completes the OAuth flow by submitting an authorization code to
// the CLI's local callback server. Accepts either:
//   - A full redirect URL: http://localhost:PORT/callback?code=X&state=Y
//     (copied from the browser error page after visiting AutoOpenURL)
//   - A code and state as: CODE#STATE
//
// The code must have been issued for the AutoOpenURL flow (redirect_uri=localhost),
// not the manual URL flow (redirect_uri=platform.claude.com).
func (p *LoginProcess) SubmitCode(code string) error {
	return p.submitCallback(context.Background(), code)
}

func (p *LoginProcess) submitCallback(ctx context.Context, code string) error {
	log := p.log()
	log.Debug("submit code", "callbackPort", p.callbackPort)

	if p.callbackPort == 0 {
		return fmt.Errorf("callback port not available (requires WithNoBrowser)")
	}

	var authCode, state string

	// Check if input is a full redirect URL.
	if isLocalhostURL(code) {
		u, err := url.Parse(code)
		if err != nil {
			return fmt.Errorf("parse redirect URL: %w", err)
		}
		authCode = u.Query().Get("code")
		state = u.Query().Get("state")
		log.Debug("submit code: parsed redirect URL")
	} else if parts := strings.SplitN(code, "#", 2); len(parts) == 2 {
		// CODE#STATE format.
		authCode = parts[0]
		state = parts[1]
		log.Debug("submit code: parsed CODE#STATE")
	} else {
		return fmt.Errorf("expected redirect URL (http://localhost:...) or CODE#STATE format")
	}

	if authCode == "" {
		return fmt.Errorf("no authorization code found in input")
	}
	if state == "" {
		return fmt.Errorf("no state parameter found in input")
	}

	callbackURL := fmt.Sprintf("http://localhost:%d/callback?code=%s&state=%s",
		p.callbackPort, url.QueryEscape(authCode), url.QueryEscape(state))
	log.Debug("submit code: calling callback", "port", p.callbackPort)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, callbackURL, nil)
	if err != nil {
		return fmt.Errorf("callback request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("callback request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	log.Debug("submit code: callback response", "status", resp.StatusCode, "contentType", resp.Header.Get("Content-Type"))

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("callback returned %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// Cancel terminates the login process. A submission waiting for the CLI's
// verdict returns at once with an error.
func (p *LoginProcess) Cancel() error {
	if p.outcome != nil {
		p.outcome.finish(errLoginCancelled)
	}
	if p.tmpDir != "" {
		os.RemoveAll(p.tmpDir)
	}
	if p.stdin != nil {
		p.stdin.Close()
	}
	if p.cmd.Process != nil {
		return p.pp.killTree(p.cmd)
	}
	return nil
}

// defaultAuthTimeout is applied when the caller's context has no deadline.
const defaultAuthTimeout = 5 * time.Second

// AuthStatus returns the current authentication state. Errors are reserved for
// infrastructure failures (binary not found, timeout). "Not logged in" is
// returned as a result with Status == AuthStateUnauthenticated, not an error.
//
// The auth commands run with the client's default WithEnv entries, like Run
// does, so a client with its own CLAUDE_CONFIG_DIR reports, logs in and logs
// out that account rather than the process's.
func (c *Client) AuthStatus(ctx context.Context) (*AuthStatusResult, error) {
	log := c.log()
	binary, err := exec.LookPath(c.binaryPath())
	if err != nil {
		return nil, fmt.Errorf("claude binary not found: %w", err)
	}
	log.Debug("auth status", "binary", binary)

	// Apply default timeout if caller didn't set one.
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultAuthTimeout)
		defer cancel()
		log.Debug("auth status: applied default timeout", "timeout", defaultAuthTimeout)
	}

	cmd := exec.CommandContext(ctx, binary, "auth", "status", "--json")
	cmd.Env = c.cliEnv().cmdEnv()
	hideConsole(cmd)
	out, err := cmd.CombinedOutput()
	log.Debug("auth status: raw output", "stdout+stderr", string(out), "cmd_err", err)

	result, parseErr := parseAuthStatus(string(out), err)
	if parseErr != nil {
		log.Debug("auth status: parse error", "error", parseErr)
	} else {
		log.Debug("auth status: result",
			"status", result.Status, "message", result.Message,
			"loggedIn", result.LoggedIn, "email", result.Email,
			"authMethod", result.AuthMethod, "subscriptionType", result.SubscriptionType)
	}
	return result, parseErr
}

// AuthLogin starts an OAuth login flow. Returns a LoginProcess with the
// authorization URL once it's available. Call Wait on the returned process
// to block until login completes.
func (c *Client) AuthLogin(ctx context.Context, opts ...AuthLoginOption) (*LoginProcess, error) {
	log := c.log()
	var cfg authLoginConfig
	for _, o := range opts {
		o(&cfg)
	}

	args := []string{"auth", "login"}
	switch cfg.method {
	case AuthMethodConsole:
		args = append(args, "--console")
	case AuthMethodClaudeAI:
		args = append(args, "--claudeai")
	}
	if cfg.sso {
		args = append(args, "--sso")
	}
	if cfg.email != "" {
		args = append(args, "--email", cfg.email)
	}

	binary, err := exec.LookPath(c.binaryPath())
	if err != nil {
		return nil, fmt.Errorf("claude binary not found: %w", err)
	}
	log.Debug("auth login", "binary", binary, "args", args,
		"noBrowser", cfg.noBrowser, "method", cfg.method, "sso", cfg.sso)

	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = c.cliEnv().cmdEnv()
	// Confine the login CLI's process tree (job object on Windows, process
	// group on unix) so cancellation cannot orphan children such as the
	// BROWSER capture script.
	pp := setPlatformAttrs(cmd)

	var tmpDir, urlFile string
	if cfg.noBrowser {
		tmpDir, err = os.MkdirTemp("", "claudecli-auth-*")
		if err != nil {
			return nil, fmt.Errorf("auth login: create temp dir: %w", err)
		}
		scriptPath, uf, err := writeBrowserCaptureScript(tmpDir)
		if err != nil {
			os.RemoveAll(tmpDir)
			return nil, fmt.Errorf("auth login: %w", err)
		}
		urlFile = uf
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		cmd.Env = append(cmd.Env, "BROWSER="+scriptPath)
		log.Debug("auth login: BROWSER capture", "script", scriptPath, "urlFile", urlFile)
	}

	// Merge stdout and stderr into one pipe we own: the URL, the prompt and
	// the verdict are split across both, and one writer keeps their order.
	// Owning the read end (rather than cmd.StdoutPipe) lets cmd.Wait return
	// at exit without closing it under the reader, so no final line is lost.
	outR, outW, err := os.Pipe()
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("auth login: output pipe: %w", err)
	}
	cmd.Stdout = outW
	cmd.Stderr = outW

	// The CLI reads the manual CODE#STATE from stdin with a line reader
	// (not its Ink TUI), so a plain pipe reaches it on every platform.
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		outR.Close()
		outW.Close()
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("auth login: stdin pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		pp.release()
		outR.Close()
		outW.Close()
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("auth login: start: %w", err)
	}
	outW.Close() // the child holds its own copy
	pp.afterStart(cmd)
	log.Debug("auth login: process started", "pid", cmd.Process.Pid)

	lp := &LoginProcess{
		tmpDir:  tmpDir,
		stdin:   stdinPipe,
		cmd:     cmd,
		pp:      pp,
		exited:  make(chan struct{}),
		outcome: newLoginOutcome(),
		logger:  c.logger,
	}

	// Scan output for the authorization URL, any localhost redirect URLs,
	// and the CLI's verdict on a submitted code.
	urlCh := make(chan string, 1)
	localhostURLCh := make(chan string, 1)
	readerDone := make(chan struct{})

	go func() {
		defer close(readerDone)
		defer outR.Close()
		scanner := bufio.NewScanner(outR)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := stripANSI(scanner.Text())
			log.Debug("auth login: output", "line", line)
			if u := extractLoginURL(line); u != "" {
				log.Debug("auth login: extracted login URL", "url", u)
				select {
				case urlCh <- u:
				default:
				}
			}
			// Also capture any URL containing a localhost redirect_uri —
			// this is the auto-open URL that carries the callback port.
			if strings.Contains(line, "localhost") {
				if u := extractFirstHTTPS(line); u != "" {
					log.Debug("auth login: found localhost URL in output", "url", u)
					select {
					case localhostURLCh <- u:
					default:
					}
				}
			}
			lp.outcome.observe(line)
		}
		if err := scanner.Err(); err != nil {
			log.Debug("auth login: scanner error", "error", err)
		}
		log.Debug("auth login: output scanner finished")
	}()

	go func() {
		err := cmd.Wait()
		pp.release()
		// The verdict line is written before exit; give the reader a moment
		// to drain it. A grandchild holding the pipe open must not stall this.
		select {
		case <-readerDone:
		case <-time.After(2 * time.Second):
		}
		lp.exitErr = err
		lp.outcome.exit(err)
		close(lp.exited)
	}()

	// Wait for either the URL, process exit, or context cancellation.
	select {
	case loginURL := <-urlCh:
		log.Debug("auth login: got login URL, extracting callback port")
		var port int
		var portSource string
		var autoOpenURL string
		// Source 1: BROWSER capture script wrote the auto-open URL to a file.
		if urlFile != "" {
			captured, err := waitForAutoURL(urlFile, 10*time.Second)
			if err != nil {
				log.Debug("auth login: BROWSER capture timeout/error", "error", err)
			} else {
				autoOpenURL = captured
				log.Debug("auth login: BROWSER captured auto-open URL", "url", autoOpenURL)
				port, err = extractCallbackPort(autoOpenURL)
				if err != nil {
					log.Debug("auth login: extractCallbackPort from auto URL failed", "error", err)
				} else {
					portSource = "browser_capture"
					log.Debug("auth login: callback port from BROWSER capture", "port", port)
				}
			}
		}
		// Source 2: CLI printed a URL containing localhost to stdout.
		if port == 0 {
			select {
			case u := <-localhostURLCh:
				log.Debug("auth login: trying localhost URL from stdout", "url", u)
				if autoOpenURL == "" {
					autoOpenURL = u
				}
				p, err := extractCallbackPort(u)
				if err != nil {
					log.Debug("auth login: extractCallbackPort from stdout URL failed", "error", err)
				} else {
					port = p
					portSource = "stdout_localhost"
					log.Debug("auth login: callback port from stdout", "port", port)
				}
			default:
				log.Debug("auth login: no localhost URL found in stdout")
			}
		}
		if port == 0 {
			log.Warn("auth login: callback port is 0 — SubmitCode will not work",
				"loginURL", loginURL)
		} else {
			log.Debug("auth login: callback port resolved", "port", port, "source", portSource)
		}
		lp.URL = loginURL
		lp.AutoOpenURL = autoOpenURL
		lp.callbackPort = port
		return lp, nil
	case <-lp.exited:
		stdinPipe.Close()
		os.RemoveAll(tmpDir)
		err := lp.exitErr
		log.Debug("auth login: process exited before URL", "error", err)
		// Process exited before we got a URL.
		if err != nil {
			return nil, fmt.Errorf("auth login: %w", err)
		}
		// Exited successfully without URL — may already be logged in.
		return nil, nil
	case <-ctx.Done():
		log.Debug("auth login: context cancelled, killing process")
		os.RemoveAll(tmpDir)
		_ = pp.killTree(cmd)
		<-lp.exited // drain to ensure process exits and pipes close
		stdinPipe.Close()
		return nil, ctx.Err()
	}
}

// AuthLogout signs out of the current Anthropic account.
func (c *Client) AuthLogout(ctx context.Context) error {
	binary, err := exec.LookPath(c.binaryPath())
	if err != nil {
		return fmt.Errorf("claude binary not found: %w", err)
	}

	cmd := exec.CommandContext(ctx, binary, "auth", "logout")
	cmd.Env = c.cliEnv().cmdEnv()
	hideConsole(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("auth logout: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// Package-level auth shortcuts using the default client.

func AuthStatus(ctx context.Context) (*AuthStatusResult, error) {
	return defaultClient.AuthStatus(ctx)
}

func AuthLogin(ctx context.Context, opts ...AuthLoginOption) (*LoginProcess, error) {
	return defaultClient.AuthLogin(ctx, opts...)
}

func AuthLogout(ctx context.Context) error {
	return defaultClient.AuthLogout(ctx)
}

// parseAuthStatus implements defensive, layered parsing of CLI auth output.
// Inspired by t3code's parseClaudeAuthStatusFromOutput — checks text patterns
// first, then JSON, then falls back to exit code interpretation.
func parseAuthStatus(output string, cmdErr error) (*AuthStatusResult, error) {
	lower := strings.ToLower(output)

	// Layer 1: text pattern matching for known error messages.
	if strings.Contains(lower, "unknown command") ||
		strings.Contains(lower, "unrecognized command") ||
		strings.Contains(lower, "unexpected argument") {
		return &AuthStatusResult{
			Status:  AuthStateUnknown,
			Message: "auth status command not supported by this CLI version",
		}, nil
	}

	if strings.Contains(lower, "not logged in") ||
		strings.Contains(lower, "login required") ||
		strings.Contains(lower, "authentication required") ||
		strings.Contains(lower, "run `claude login`") ||
		strings.Contains(lower, "run claude login") {
		return &AuthStatusResult{
			Status:  AuthStateUnauthenticated,
			Message: "not logged in",
		}, nil
	}

	// Layer 2: JSON parsing with flexible key extraction.
	trimmed := strings.TrimSpace(output)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		var raw map[string]any
		if err := json.Unmarshal([]byte(trimmed), &raw); err == nil {
			result := &AuthStatusResult{}

			// Extract auth boolean from multiple possible keys.
			if v, ok := extractJSONBool(raw, "loggedIn", "isLoggedIn", "authenticated", "isAuthenticated"); ok {
				result.LoggedIn = v
				if v {
					result.Status = AuthStateAuthenticated
				} else {
					result.Status = AuthStateUnauthenticated
					result.Message = "not logged in"
				}
			} else {
				// JSON parsed but no auth marker found.
				result.Status = AuthStateUnknown
				result.Message = "auth status JSON missing auth marker"
			}

			// Extract string fields from multiple possible keys.
			result.AuthMethod = extractJSONString(raw, "authMethod", "auth_method")
			result.APIProvider = extractJSONString(raw, "apiProvider", "api_provider")
			result.Email = extractJSONString(raw, "email")
			result.OrgID = extractJSONString(raw, "orgId", "org_id")
			result.OrgName = extractJSONString(raw, "orgName", "org_name")
			result.SubscriptionType = extractJSONString(raw, "subscriptionType", "subscription_type", "plan", "tier")

			return result, nil
		}
	}

	// Layer 3: exit code fallback.
	if cmdErr == nil {
		// Exited 0 but output wasn't JSON — cannot determine auth state.
		// Fail-close: treat as unknown rather than assuming authenticated.
		return &AuthStatusResult{
			Status:  AuthStateUnknown,
			Message: "exit 0, non-JSON output",
		}, nil
	}

	// Non-zero exit with unrecognized output.
	return &AuthStatusResult{
		Status:  AuthStateUnknown,
		Message: fmt.Sprintf("auth status failed: %v", cmdErr),
	}, nil
}

// extractJSONBool looks for the first matching boolean key in a JSON object.
func extractJSONBool(m map[string]any, keys ...string) (bool, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if b, ok := v.(bool); ok {
				return b, true
			}
		}
	}
	return false, false
}

// extractJSONString looks for the first matching non-empty string key.
func extractJSONString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// oauthURLRe matches OAuth-style authorization URLs.
var oauthURLRe = regexp.MustCompile(`https://[^\s]+/oauth/authorize[^\s]*`)

// extractLoginURL extracts the authorization URL from a CLI output line.
// Tries the known prefix first, then falls back to pattern matching.
func extractLoginURL(line string) string {
	// Primary: exact prefix used by current CLI versions.
	const prefix = "If the browser didn't open, visit: "
	if _, after, ok := strings.Cut(line, prefix); ok {
		return strings.TrimSpace(after)
	}

	// Fallback 1: line contains "visit" or "open" with an https URL.
	lower := strings.ToLower(line)
	if strings.Contains(lower, "visit") || strings.Contains(lower, "open") {
		if u := extractFirstHTTPS(line); u != "" {
			return u
		}
	}

	// Fallback 2: OAuth authorize URL anywhere in the line.
	if m := oauthURLRe.FindString(line); m != "" {
		return m
	}

	return ""
}

// extractFirstHTTPS returns the first https:// URL found in the line.
func extractFirstHTTPS(line string) string {
	idx := strings.Index(line, "https://")
	if idx < 0 {
		return ""
	}
	rest := line[idx:]
	// URL ends at whitespace.
	if end := strings.IndexAny(rest, " \t\r\n"); end > 0 {
		return rest[:end]
	}
	return rest
}

// extractCallbackPort parses the CLI's auto-open URL and returns the port from
// its redirect_uri parameter (e.g. redirect_uri=http://localhost:PORT/callback).
func extractCallbackPort(autoURL string) (int, error) {
	u, err := url.Parse(autoURL)
	if err != nil {
		return 0, fmt.Errorf("parse auto URL: %w", err)
	}
	redirectURI := u.Query().Get("redirect_uri")
	if redirectURI == "" {
		return 0, fmt.Errorf("no redirect_uri in auto URL")
	}
	ru, err := url.Parse(redirectURI)
	if err != nil {
		return 0, fmt.Errorf("parse redirect_uri: %w", err)
	}
	portStr := ru.Port()
	if portStr == "" {
		return 0, fmt.Errorf("no port in redirect_uri %q", redirectURI)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("invalid port %q: %w", portStr, err)
	}
	return port, nil
}

// waitForAutoURL polls for the browser capture file to appear and returns its
// contents. The CLI writes the auto-open URL to this file via the BROWSER script.
func waitForAutoURL(urlFile string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(urlFile)
		if err == nil && len(data) > 0 {
			return string(data), nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "", fmt.Errorf("timed out waiting for browser URL capture")
}
