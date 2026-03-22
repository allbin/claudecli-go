package claudecli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// staticExecutor returns a fixed stdout response. Used to test blocking mode
// without JSONL parsing.
type staticExecutor struct {
	stdout []byte
	stderr string
}

func (e *staticExecutor) Start(_ context.Context, _ *StartConfig) (*Process, error) {
	return &Process{
		Stdout: io.NopCloser(bytes.NewReader(e.stdout)),
		Stderr: io.NopCloser(strings.NewReader(e.stderr)),
		Wait:   func() error { return nil },
	}, nil
}

const blockingFixture = `{
	"type": "result",
	"subtype": "success",
	"result": "Hello, world!",
	"session_id": "abc-123",
	"total_cost_usd": 0.005,
	"duration_ms": 1234,
	"num_turns": 1,
	"is_error": false,
	"usage": {
		"input_tokens": 100,
		"output_tokens": 50,
		"cache_read_input_tokens": 10,
		"cache_creation_input_tokens": 5
	}
}`

const blockingStructuredFixture = `{
	"type": "result",
	"subtype": "success",
	"result": "raw text output",
	"structured_output": {"name": "test", "count": 42},
	"session_id": "abc-456",
	"total_cost_usd": 0.01,
	"duration_ms": 2000,
	"num_turns": 1,
	"is_error": false,
	"usage": {"input_tokens": 200, "output_tokens": 100}
}`

func TestRunBlocking(t *testing.T) {
	client := NewWithExecutor(&staticExecutor{stdout: []byte(blockingFixture)})

	result, err := client.RunBlocking(context.Background(), "ignored")
	if err != nil {
		t.Fatal(err)
	}

	if result.Text != "Hello, world!" {
		t.Errorf("text = %q, want %q", result.Text, "Hello, world!")
	}
	if result.SessionID != "abc-123" {
		t.Errorf("session_id = %q", result.SessionID)
	}
	if result.CostUSD != 0.005 {
		t.Errorf("cost = %f", result.CostUSD)
	}
	if result.NumTurns != 1 {
		t.Errorf("num_turns = %d", result.NumTurns)
	}
	if result.Usage.InputTokens != 100 {
		t.Errorf("input_tokens = %d", result.Usage.InputTokens)
	}
	if result.Usage.CacheReadTokens != 10 {
		t.Errorf("cache_read_tokens = %d", result.Usage.CacheReadTokens)
	}
}

func TestRunBlockingJSON(t *testing.T) {
	client := NewWithExecutor(&staticExecutor{stdout: []byte(blockingFixture)})

	type Greeting struct {
		// This won't parse since the result is plain text "Hello, world!"
	}

	// Test with plain text result — should fail to unmarshal as JSON
	_, _, err := RunBlockingJSON[map[string]any](context.Background(), client, "ignored")
	if err == nil {
		t.Fatal("expected unmarshal error for non-JSON text")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestRunBlockingJSONStructuredOutput(t *testing.T) {
	client := NewWithExecutor(&staticExecutor{stdout: []byte(blockingStructuredFixture)})

	type Result struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}

	val, result, err := RunBlockingJSON[Result](context.Background(), client, "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if val.Name != "test" {
		t.Errorf("name = %q", val.Name)
	}
	if val.Count != 42 {
		t.Errorf("count = %d", val.Count)
	}
	if result.Text != "raw text output" {
		t.Errorf("text = %q", result.Text)
	}
}

// CLI may return a JSON array wrapping the result object.
const blockingArrayFixture = `[
	{"type": "system", "session_id": "abc-789"},
	{
		"type": "result",
		"subtype": "success",
		"result": "array response",
		"session_id": "abc-789",
		"total_cost_usd": 0.003,
		"duration_ms": 500,
		"num_turns": 1,
		"is_error": false,
		"usage": {"input_tokens": 50, "output_tokens": 25}
	}
]`

func TestRunBlockingArray(t *testing.T) {
	client := NewWithExecutor(&staticExecutor{stdout: []byte(blockingArrayFixture)})

	result, err := client.RunBlocking(context.Background(), "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "array response" {
		t.Errorf("text = %q, want %q", result.Text, "array response")
	}
	if result.SessionID != "abc-789" {
		t.Errorf("session_id = %q", result.SessionID)
	}
	if result.CostUSD != 0.003 {
		t.Errorf("cost = %f", result.CostUSD)
	}
	if result.Usage.InputTokens != 50 {
		t.Errorf("input_tokens = %d", result.Usage.InputTokens)
	}
}

func TestRunBlockingJSONArray(t *testing.T) {
	fixture := `[
		{"type": "system", "session_id": "s1"},
		{
			"type": "result",
			"subtype": "success",
			"result": "{\"name\":\"from-array\",\"count\":99}",
			"session_id": "s1",
			"total_cost_usd": 0.002,
			"duration_ms": 300,
			"num_turns": 1,
			"is_error": false,
			"usage": {"input_tokens": 30, "output_tokens": 15}
		}
	]`
	client := NewWithExecutor(&staticExecutor{stdout: []byte(fixture)})

	type Result struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}

	val, _, err := RunBlockingJSON[Result](context.Background(), client, "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if val.Name != "from-array" {
		t.Errorf("name = %q", val.Name)
	}
	if val.Count != 99 {
		t.Errorf("count = %d", val.Count)
	}
}

func TestRunBlockingStartFailure(t *testing.T) {
	client := NewWithExecutor(&failExecutor{err: errors.New("connection refused")})

	_, err := client.RunBlocking(context.Background(), "ignored")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRunBlockingEmptyResponse(t *testing.T) {
	exec := &staticExecutor{stdout: []byte{}}
	client := NewWithExecutor(exec)
	_, err := client.RunBlocking(context.Background(), "ignored")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "empty response") {
		t.Errorf("expected 'empty response' error, got: %v", err)
	}
}

func TestRunBlockingStderrCallback(t *testing.T) {
	exec := &staticExecutor{
		stdout: []byte(blockingFixture),
		stderr: "warn: rate limit\nerror: retry\n",
	}
	client := NewWithExecutor(exec)

	var lines []string
	result, err := client.RunBlocking(context.Background(), "ignored",
		WithStderrCallback(func(line string) {
			lines = append(lines, line)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 callback lines, got %d", len(lines))
	}
	if lines[0] != "warn: rate limit" {
		t.Errorf("line 0 = %q", lines[0])
	}
	if lines[1] != "error: retry" {
		t.Errorf("line 1 = %q", lines[1])
	}
	if result.Stderr != "warn: rate limit\nerror: retry" {
		t.Errorf("Stderr = %q", result.Stderr)
	}
}

func TestRunBlockingStderrPopulated(t *testing.T) {
	exec := &staticExecutor{
		stdout: []byte(blockingFixture),
		stderr: "some warning\n",
	}
	client := NewWithExecutor(exec)

	result, err := client.RunBlocking(context.Background(), "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if result.Stderr != "some warning" {
		t.Errorf("Stderr = %q, want %q", result.Stderr, "some warning")
	}
}

// Fix #1: Concurrent stdout/stderr reads — large stderr shouldn't deadlock.
func TestRunBlockingLargeStderr(t *testing.T) {
	// Generate large stderr that would deadlock if read sequentially after stdout.
	var bigStderr strings.Builder
	for i := 0; i < 10000; i++ {
		bigStderr.WriteString("warning: this is line " + strings.Repeat("x", 100) + "\n")
	}

	exec := &staticExecutor{
		stdout: []byte(blockingFixture),
		stderr: bigStderr.String(),
	}
	client := NewWithExecutor(exec)

	done := make(chan struct{})
	go func() {
		defer close(done)
		result, err := client.RunBlocking(context.Background(), "ignored")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if result.Text != "Hello, world!" {
			t.Errorf("text = %q", result.Text)
		}
		if result.Stderr == "" {
			t.Error("expected non-empty stderr")
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunBlocking deadlocked with large stderr")
	}
}

// processExitError always parses stderr for structured error details,
// regardless of whether the underlying error is an *exec.ExitError.
func TestProcessExitError_NonExitErrorWithDetails(t *testing.T) {
	stderr := `{"type":"rate_limit","message":"Rate limit exceeded","retry_after_seconds":30}`
	err := processExitError(errors.New("generic error"), stderr)
	if err.ExitCode != -1 {
		t.Errorf("exit code = %d, want -1", err.ExitCode)
	}
	if err.Message != "Rate limit exceeded" {
		t.Errorf("message = %q, want 'Rate limit exceeded'", err.Message)
	}
	if err.Details == nil {
		t.Fatal("expected non-nil Details")
	}
	if !err.IsRateLimit() {
		t.Error("expected IsRateLimit() = true")
	}
}

func TestProcessExitError_NonExitErrorPlainStderr(t *testing.T) {
	err := processExitError(errors.New("broken pipe"), "some stderr")
	if err.ExitCode != -1 {
		t.Errorf("exit code = %d, want -1", err.ExitCode)
	}
	if err.Message != "broken pipe" {
		t.Errorf("message = %q, want 'broken pipe'", err.Message)
	}
	if err.Stderr != "some stderr" {
		t.Errorf("stderr = %q", err.Stderr)
	}
}

func TestProcessExitError_NonExitErrorDetailsMessage(t *testing.T) {
	stderr := `{"type":"auth","message":"Invalid API key"}`
	err := processExitError(errors.New("exit status 1"), stderr)
	if err.Message != "Invalid API key" {
		t.Errorf("message = %q, want 'Invalid API key'", err.Message)
	}
	if err.Details == nil {
		t.Fatal("expected non-nil Details")
	}
	if !err.IsAuth() {
		t.Error("expected IsAuth() = true")
	}
}

// Fix #6: rawUsage.toUsage() conversion.
func TestRawUsageToUsage(t *testing.T) {
	raw := rawUsage{
		InputTokens:              100,
		OutputTokens:             50,
		CacheReadInputTokens:     25,
		CacheCreationInputTokens: 10,
	}
	u := raw.toUsage()
	if u.InputTokens != 100 {
		t.Errorf("InputTokens = %d", u.InputTokens)
	}
	if u.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d", u.OutputTokens)
	}
	if u.CacheReadTokens != 25 {
		t.Errorf("CacheReadTokens = %d", u.CacheReadTokens)
	}
	if u.CacheCreateTokens != 10 {
		t.Errorf("CacheCreateTokens = %d", u.CacheCreateTokens)
	}
}

func TestRawUsageToUsage_Zero(t *testing.T) {
	raw := rawUsage{}
	u := raw.toUsage()
	if u.InputTokens != 0 || u.OutputTokens != 0 || u.CacheReadTokens != 0 || u.CacheCreateTokens != 0 {
		t.Errorf("expected all zeros, got %+v", u)
	}
}
