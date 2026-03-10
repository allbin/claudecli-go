package claudecli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Client wraps a Claude CLI executor with default options.
type Client struct {
	executor Executor
	defaults []Option
}

// New creates a client. Pass options to set defaults for all calls.
// Use WithBinaryPath to override the CLI binary location.
func New(defaults ...Option) *Client {
	resolved := resolveOptions(defaults, nil)
	executor := NewLocalExecutor()
	if resolved.binaryPath != "" {
		executor.BinaryPath = resolved.binaryPath
	}
	return &Client{
		executor: executor,
		defaults: defaults,
	}
}

// NewWithExecutor creates a client with a specific executor and default options.
func NewWithExecutor(executor Executor, defaults ...Option) *Client {
	return &Client{
		executor: executor,
		defaults: defaults,
	}
}

// Run starts a streaming Claude session. Returns a Stream for event consumption.
// The executor is started synchronously so the process is running when Run returns.
// Callers can safely defer cleanup of resources the process depends on (temp files, mounts).
func (c *Client) Run(ctx context.Context, prompt string, opts ...Option) *Stream {
	ctx, cancel := context.WithCancel(ctx)
	resolved := resolveOptions(c.defaults, opts)
	args := resolved.buildArgs()

	events := make(chan Event, 64)
	done := make(chan struct{})
	stream := newStream(ctx, events, done, cancel)

	// Emit StartEvent before process launch
	events <- &StartEvent{
		Model:   resolved.model,
		Args:    args,
		WorkDir: resolved.workDir,
	}

	proc, err := c.executor.Start(ctx, &StartConfig{
		Args:                    args,
		Stdin:                   strings.NewReader(prompt),
		Env:                     resolved.env,
		WorkDir:                 resolved.workDir,
		EnableFileCheckpointing: resolved.enableFileCheckpointing,
		SkipVersionCheck:        resolved.skipVersionCheck,
	})
	if err != nil {
		events <- &ErrorEvent{Err: fmt.Errorf("start: %w", err), Fatal: true}
		close(events)
		close(done)
		return stream
	}

	go func() {
		defer close(done)
		defer close(events)
		c.readProcess(proc, events, resolved.stderrCallback)
	}()

	return stream
}

func (c *Client) readProcess(proc *Process, events chan<- Event, stderrCallback func(string)) {
	stderrLines, stderrDone := scanStderr(proc, events, stderrCallback)

	// Intercept parsed events to track whether a ResultEvent was emitted
	parsed := make(chan Event, 64)
	var sawResult bool
	var accText []string
	parseDone := make(chan struct{})
	go func() {
		defer close(parseDone)
		for ev := range parsed {
			switch e := ev.(type) {
			case *ResultEvent:
				sawResult = true
			case *TextEvent:
				accText = append(accText, e.Content)
			}
			events <- ev
		}
	}()

	ParseEvents(proc.Stdout, parsed)
	close(parsed)
	<-parseDone
	<-stderrDone

	if err := proc.Wait(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			events <- &ErrorEvent{Err: err, Fatal: true}
			return
		}
		stderr := strings.Join(*stderrLines, "\n")
		events <- &ErrorEvent{
			Err: &Error{
				ExitCode: exitErr.ExitCode(),
				Stderr:   stderr,
				Details:  parseErrorDetails(stderr),
			},
			Fatal: true,
		}
		return
	}

	// Synthesize ResultEvent if CLI exited successfully without emitting one
	if !sawResult {
		events <- &ResultEvent{
			Text: strings.Join(accText, ""),
		}
	}
}

func scanStderr(proc *Process, events chan<- Event, callback func(string)) (*[]string, <-chan struct{}) {
	var lines []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				events <- &ErrorEvent{
					Err:   fmt.Errorf("stderr goroutine panic: %v", r),
					Fatal: true,
				}
			}
		}()
		scanner := bufio.NewScanner(proc.Stderr)
		for scanner.Scan() {
			line := scanner.Text()
			lines = append(lines, line)
			if callback != nil {
				callback(line)
			}
			events <- &StderrEvent{Content: line}
		}
	}()
	return &lines, done
}

// RunText runs a prompt and returns the accumulated text output.
func (c *Client) RunText(ctx context.Context, prompt string, opts ...Option) (string, *ResultEvent, error) {
	stream := c.Run(ctx, prompt, opts...)
	result, err := stream.Wait()
	if err != nil {
		return "", result, err
	}
	if result == nil {
		return "", nil, ErrEmptyOutput
	}
	return result.Text, result, nil
}

// RunJSON runs a prompt and unmarshals the text output into T.
// Markdown code fences (```json ... ``` or ``` ... ```) are stripped
// before unmarshaling so that model responses wrapped in fences parse correctly.
func RunJSON[T any](ctx context.Context, c *Client, prompt string, opts ...Option) (T, *ResultEvent, error) {
	var zero T
	text, result, err := c.RunText(ctx, prompt, opts...)
	if err != nil {
		return zero, result, err
	}
	if err := json.Unmarshal([]byte(stripCodeFence(text)), &zero); err != nil {
		return zero, result, &UnmarshalError{Err: err, RawText: text}
	}
	return zero, result, nil
}

// stripCodeFence removes surrounding markdown code fences from text.
// Handles ```json\n...\n```, ```\n...\n```, and leading/trailing whitespace.
// Only matches exactly three backticks optionally followed by a language tag
// (letters/digits only) — four+ backticks or non-alphanumeric suffixes are ignored.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	lines := strings.SplitN(s, "\n", 2)
	if len(lines) < 2 {
		return s
	}
	first := strings.TrimSpace(lines[0])
	if !isOpeningFence(first) {
		return s
	}
	// Find closing fence (may not be last line if model appends commentary)
	rest := s[strings.Index(s, "\n")+1:]
	fenceIdx := -1
	pos := 0
	for {
		nl := strings.Index(rest[pos:], "\n")
		var line string
		if nl < 0 {
			line = rest[pos:]
		} else {
			line = rest[pos : pos+nl]
		}
		if strings.TrimSpace(line) == "```" {
			fenceIdx = pos
			break
		}
		if nl < 0 {
			break
		}
		pos += nl + 1
	}
	if fenceIdx < 0 {
		return s
	}
	// Extract content between fences
	inner := rest[:fenceIdx]
	return strings.TrimSpace(inner)
}

// isOpeningFence returns true for exactly ``` or ```<alphanum lang tag>.
func isOpeningFence(line string) bool {
	if !strings.HasPrefix(line, "```") {
		return false
	}
	tag := line[3:]
	if tag == "" {
		return true
	}
	// Reject 4+ backticks or non-alphanumeric suffixes
	for _, r := range tag {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// Connect starts an interactive session with bidirectional control protocol.
// Returns a Session for multi-turn conversations, permission callbacks, etc.
func (c *Client) Connect(ctx context.Context, opts ...Option) (*Session, error) {
	ctx, cancel := context.WithCancel(ctx)
	resolved := resolveOptions(c.defaults, opts)
	args := resolved.buildSessionArgs()

	proc, err := c.executor.Start(ctx, &StartConfig{
		Args:                    args,
		Env:                     resolved.env,
		WorkDir:                 resolved.workDir,
		KeepStdinOpen:           true,
		EnableFileCheckpointing: resolved.enableFileCheckpointing,
		SkipVersionCheck:        resolved.skipVersionCheck,
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("start: %w", err)
	}

	session := &Session{
		proc:       proc,
		events:     make(chan Event, 64),
		done:       make(chan struct{}),
		ctx:        ctx,
		cancel:     cancel,
		canUseTool: resolved.canUseTool,
	}

	go session.readLoop()

	if err := session.initialize(); err != nil {
		cancel()
		return nil, fmt.Errorf("initialize: %w", err)
	}

	return session, nil
}

// Package-level shortcuts for one-off use without constructing a client.

var defaultClient = New()

// Run starts a streaming session using the default local executor.
func Run(ctx context.Context, prompt string, opts ...Option) *Stream {
	return defaultClient.Run(ctx, prompt, opts...)
}

// RunText runs a prompt and returns text using the default local executor.
func RunText(ctx context.Context, prompt string, opts ...Option) (string, *ResultEvent, error) {
	return defaultClient.RunText(ctx, prompt, opts...)
}

// Connect starts an interactive session using the default local executor.
func Connect(ctx context.Context, opts ...Option) (*Session, error) {
	return defaultClient.Connect(ctx, opts...)
}
