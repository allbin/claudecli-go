package claudecli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// staticExecutor returns a fixed stdout response. Used to test blocking mode
// without JSONL parsing.
type staticExecutor struct {
	stdout []byte
}

func (e *staticExecutor) Start(_ context.Context, _ *StartConfig) (*Process, error) {
	return &Process{
		Stdout: io.NopCloser(bytes.NewReader(e.stdout)),
		Stderr: io.NopCloser(strings.NewReader("")),
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

func TestRunBlockingStartFailure(t *testing.T) {
	client := NewWithExecutor(&failExecutor{err: errors.New("connection refused")})

	_, err := client.RunBlocking(context.Background(), "ignored")
	if err == nil {
		t.Fatal("expected error")
	}
}
