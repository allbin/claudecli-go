package claudecli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// AuthStatusResult represents the authentication state returned by the CLI.
type AuthStatusResult struct {
	LoggedIn         bool   `json:"loggedIn"`
	AuthMethod       string `json:"authMethod,omitempty"`       // e.g. "claude.ai", "api-key"
	APIProvider      string `json:"apiProvider,omitempty"`       // e.g. "firstParty", "bedrock", "vertex"
	Email            string `json:"email,omitempty"`
	OrgID            string `json:"orgId,omitempty"`
	OrgName          string `json:"orgName,omitempty"`
	SubscriptionType string `json:"subscriptionType,omitempty"` // e.g. "team", "pro"
}

// AuthMethod selects the authentication provider for login.
type AuthMethod string

const (
	AuthMethodClaudeAI AuthMethod = "claudeai" // Claude subscription (default)
	AuthMethodConsole  AuthMethod = "console"   // Anthropic Console (API billing)
)

// authLoginConfig holds resolved login options.
type authLoginConfig struct {
	method AuthMethod
	sso    bool
	email  string
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

// LoginProcess represents an in-progress OAuth login.
// The URL field contains the authorization URL for the user to visit.
// Call Wait to block until the login completes or the context is cancelled.
type LoginProcess struct {
	URL  string
	cmd  *exec.Cmd
	done chan error
}

// Wait blocks until the login process completes. Returns nil on success.
func (p *LoginProcess) Wait() error {
	return <-p.done
}

// Cancel terminates the login process.
func (p *LoginProcess) Cancel() error {
	if p.cmd.Process != nil {
		return p.cmd.Process.Kill()
	}
	return nil
}

// AuthStatus returns the current authentication state.
func (c *Client) AuthStatus(ctx context.Context) (*AuthStatusResult, error) {
	binary, err := exec.LookPath(c.binaryPath())
	if err != nil {
		return nil, fmt.Errorf("claude binary not found: %w", err)
	}

	out, err := exec.CommandContext(ctx, binary, "auth", "status", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("auth status: %w", err)
	}

	var status AuthStatusResult
	if err := json.Unmarshal(out, &status); err != nil {
		return nil, fmt.Errorf("auth status: parse: %w", err)
	}
	return &status, nil
}

// AuthLogin starts an OAuth login flow. Returns a LoginProcess with the
// authorization URL once it's available. Call Wait on the returned process
// to block until login completes.
func (c *Client) AuthLogin(ctx context.Context, opts ...AuthLoginOption) (*LoginProcess, error) {
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

	cmd := exec.CommandContext(ctx, binary, args...)

	// Merge stdout and stderr — the URL may appear on either.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("auth login: stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout // merge stderr into stdout pipe

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("auth login: start: %w", err)
	}

	// Scan output for the authorization URL.
	urlCh := make(chan string, 1)
	doneCh := make(chan error, 1)

	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if url := extractLoginURL(line); url != "" {
				urlCh <- url
			}
		}
	}()

	go func() {
		doneCh <- cmd.Wait()
	}()

	// Wait for either the URL, process exit, or context cancellation.
	select {
	case url := <-urlCh:
		lp := &LoginProcess{URL: url, cmd: cmd, done: doneCh}
		return lp, nil
	case err := <-doneCh:
		// Process exited before we got a URL.
		if err != nil {
			return nil, fmt.Errorf("auth login: %w", err)
		}
		// Exited successfully without URL — may already be logged in.
		return nil, nil
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return nil, ctx.Err()
	}
}

// AuthLogout signs out of the current Anthropic account.
func (c *Client) AuthLogout(ctx context.Context) error {
	binary, err := exec.LookPath(c.binaryPath())
	if err != nil {
		return fmt.Errorf("claude binary not found: %w", err)
	}

	out, err := exec.CommandContext(ctx, binary, "auth", "logout").CombinedOutput()
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

// extractLoginURL extracts the authorization URL from a CLI output line.
func extractLoginURL(line string) string {
	const prefix = "If the browser didn't open, visit: "
	if _, after, ok := strings.Cut(line, prefix); ok {
		return strings.TrimSpace(after)
	}
	return ""
}

