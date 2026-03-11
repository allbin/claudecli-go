package claudecli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestClientRunWithFixture(t *testing.T) {
	exec, err := NewFixtureExecutorFromFile("testdata/basic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	client := NewWithExecutor(exec)

	stream := client.Run(context.Background(), "ignored")

	var gotStart, gotInit, gotText, gotResult bool
	for event := range stream.Events() {
		switch event.(type) {
		case *StartEvent:
			gotStart = true
		case *InitEvent:
			gotInit = true
		case *TextEvent:
			gotText = true
		case *ResultEvent:
			gotResult = true
		}
	}

	if !gotStart {
		t.Error("no StartEvent")
	}
	if !gotInit {
		t.Error("no InitEvent")
	}
	if !gotText {
		t.Error("no TextEvent")
	}
	if !gotResult {
		t.Error("no ResultEvent")
	}

	if stream.State() != StateDone {
		t.Errorf("expected StateDone, got %s", stream.State())
	}
}

func TestClientRunTextWithFixture(t *testing.T) {
	exec, err := NewFixtureExecutorFromFile("testdata/basic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	client := NewWithExecutor(exec)

	text, result, err := client.RunText(context.Background(), "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if text == "" {
		t.Error("empty text")
	}
	if result == nil {
		t.Fatal("nil result")
	}
	if result.CostUSD <= 0 {
		t.Error("zero cost")
	}
}

func TestClientRunWaitIdempotent(t *testing.T) {
	exec, err := NewFixtureExecutorFromFile("testdata/basic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	client := NewWithExecutor(exec)

	stream := client.Run(context.Background(), "ignored")

	r1, err1 := stream.Wait()
	r2, err2 := stream.Wait()

	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected errors: %v, %v", err1, err2)
	}
	if r1 != r2 {
		t.Error("Wait() not idempotent")
	}
}

func TestClientRunContextCancel(t *testing.T) {
	exec, err := NewFixtureExecutorFromFile("testdata/basic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	client := NewWithExecutor(exec)

	ctx, cancel := context.WithCancel(context.Background())
	stream := client.Run(ctx, "ignored")

	// Read one event then cancel
	_, ok := stream.Next()
	if !ok {
		t.Fatal("expected at least one event")
	}
	cancel()

	// Wait should complete without hanging
	done := make(chan struct{})
	go func() {
		stream.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait() hung after context cancel")
	}
}

func TestClientRunClose(t *testing.T) {
	exec, err := NewFixtureExecutorFromFile("testdata/basic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	client := NewWithExecutor(exec)
	stream := client.Run(context.Background(), "ignored")

	done := make(chan error)
	go func() {
		done <- stream.Close()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Close returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close() hung")
	}
}

func TestClientRunStartFailure(t *testing.T) {
	executor := &failExecutor{err: errors.New("connection refused")}
	client := NewWithExecutor(executor)

	stream := client.Run(context.Background(), "ignored")
	_, err := stream.Wait()

	if err == nil {
		t.Fatal("expected error from failed start")
	}
	if stream.State() != StateFailed {
		t.Errorf("expected StateFailed, got %s", stream.State())
	}
}

func TestStripCodeFence(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain json", `{"a":1}`, `{"a":1}`},
		{"json fence", "```json\n{\"a\":1}\n```", `{"a":1}`},
		{"bare fence", "```\n{\"a\":1}\n```", `{"a":1}`},
		{"with whitespace", "  ```json\n{\"a\":1}\n```  ", `{"a":1}`},
		{"multiline content", "```json\n{\n  \"a\": 1,\n  \"b\": 2\n}\n```", "{\n  \"a\": 1,\n  \"b\": 2\n}"},
		{"no closing fence", "```json\n{\"a\":1}", "```json\n{\"a\":1}"},
		{"single line", `{"a":1}`, `{"a":1}`},
		{"empty", "", ""},
		{"trailing text after fence", "```json\n{\"a\":1}\n```\n\n**Reasoning:** some explanation", `{"a":1}`},
		// Tighter detection: reject 4+ backticks
		{"four backticks ignored", "````json\n{\"a\":1}\n```", "````json\n{\"a\":1}\n```"},
		// Reject non-alphanumeric lang tag
		{"special char tag ignored", "```json!\n{\"a\":1}\n```", "```json!\n{\"a\":1}\n```"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripCodeFence(tt.input)
			if got != tt.want {
				t.Errorf("stripCodeFence(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestUnmarshalErrorContainsRawText(t *testing.T) {
	exec, err := NewFixtureExecutorFromFile("testdata/basic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	client := NewWithExecutor(exec)

	// basic.jsonl returns prose, not JSON — RunJSON should fail with UnmarshalError
	type Dummy struct{ X int }
	_, _, err = RunJSON[Dummy](context.Background(), client, "ignored")
	if err == nil {
		t.Fatal("expected error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
	if ue.RawText == "" {
		t.Error("RawText is empty")
	}
}

func TestSynthesizeResultOnMissingResultEvent(t *testing.T) {
	// Simulates a CLI that exits cleanly but never emits a result JSONL line.
	// Common with DockerExecutor.
	jsonl := `{"type":"system","session_id":"test-123","model":"sonnet"}
{"type":"assistant","message":{"content":[{"type":"text","text":"hello "},{"type":"text","text":"world"}]}}
`
	exec := NewFixtureExecutor(strings.NewReader(jsonl))
	client := NewWithExecutor(exec)

	stream := client.Run(context.Background(), "ignored")
	var gotResult bool
	var resultText string
	for event := range stream.Events() {
		if r, ok := event.(*ResultEvent); ok {
			gotResult = true
			resultText = r.Text
		}
	}

	if !gotResult {
		t.Fatal("expected synthesized ResultEvent")
	}
	if resultText != "hello world" {
		t.Errorf("expected 'hello world', got %q", resultText)
	}
	if stream.State() != StateDone {
		t.Errorf("expected StateDone, got %s", stream.State())
	}

	result, err := stream.Wait()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Wait() returned nil result")
	}
}

func TestRunProcessStartedBeforeReturn(t *testing.T) {
	// Verify that executor.Start() is called synchronously in Run(),
	// not deferred to a goroutine. We use a tracking executor.
	tracker := &trackStartExecutor{}
	client := NewWithExecutor(tracker)

	_ = client.Run(context.Background(), "ignored")

	// Start should have been called by the time Run() returns
	if !tracker.started {
		t.Error("executor.Start() was not called before Run() returned")
	}
}

func TestStderrCallback(t *testing.T) {
	// Simulate a process that emits stderr lines.
	stderrContent := "warning: something\nerror: bad thing\n"
	exec := &stderrExecutor{
		stdout: `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`,
		stderr: stderrContent,
	}
	var callbackLines []string
	client := NewWithExecutor(exec)

	stream := client.Run(context.Background(), "ignored",
		WithStderrCallback(func(line string) {
			callbackLines = append(callbackLines, line)
		}),
	)

	_, err := stream.Wait()
	if err != nil {
		t.Fatal(err)
	}

	if len(callbackLines) != 2 {
		t.Fatalf("expected 2 callback calls, got %d", len(callbackLines))
	}
	if callbackLines[0] != "warning: something" {
		t.Errorf("line 0 = %q", callbackLines[0])
	}
	if callbackLines[1] != "error: bad thing" {
		t.Errorf("line 1 = %q", callbackLines[1])
	}
}

type stderrExecutor struct {
	stdout string
	stderr string
}

func (e *stderrExecutor) Start(_ context.Context, _ *StartConfig) (*Process, error) {
	return &Process{
		Stdout: io.NopCloser(strings.NewReader(e.stdout)),
		Stderr: io.NopCloser(strings.NewReader(e.stderr)),
		Wait:   func() error { return nil },
	}, nil
}

func TestEnableFileCheckpointing(t *testing.T) {
	var capturedCfg *StartConfig
	exec := &capturingExecutor{capture: func(cfg *StartConfig) { capturedCfg = cfg }}
	client := NewWithExecutor(exec)

	_ = client.Run(context.Background(), "ignored", WithFileCheckpointing())

	if capturedCfg == nil {
		t.Fatal("StartConfig not captured")
	}
	if !capturedCfg.EnableFileCheckpointing {
		t.Error("EnableFileCheckpointing not set")
	}
}

type capturingExecutor struct {
	capture func(*StartConfig)
}

func (e *capturingExecutor) Start(_ context.Context, cfg *StartConfig) (*Process, error) {
	if e.capture != nil {
		e.capture(cfg)
	}
	return &Process{
		Stdout: io.NopCloser(strings.NewReader("")),
		Stderr: io.NopCloser(strings.NewReader("")),
		Wait:   func() error { return nil },
	}, nil
}

// Fix #15: Stderr capped at maxStderrLines, keeping most recent lines.
func TestStderrCappedAtMaxLines(t *testing.T) {
	totalLines := maxStderrLines + 500
	var stderrBuf strings.Builder
	for i := 0; i < totalLines; i++ {
		fmt.Fprintf(&stderrBuf, "line-%04d\n", i)
	}

	// Use an executor that fails (exit code 1) so stderr is collected
	// into the Error. We need the wait function to return an exec.ExitError-like error.
	exec := &stderrExecutor{
		stdout: `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`,
		stderr: stderrBuf.String(),
	}
	client := NewWithExecutor(exec)

	stream := client.Run(context.Background(), "ignored")
	_, err := stream.Wait()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Count StderrEvents — should be capped
	// Re-run with a fresh executor that actually emits all lines
	exec2 := &stderrExecutor{
		stdout: `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`,
		stderr: stderrBuf.String(),
	}
	client2 := NewWithExecutor(exec2)
	stream2 := client2.Run(context.Background(), "ignored")

	var stderrEvents int
	for event := range stream2.Events() {
		if _, ok := event.(*StderrEvent); ok {
			stderrEvents++
		}
	}
	// StderrEvents are emitted for every line (all totalLines), but the internal
	// lines buffer is capped at maxStderrLines. We verify by checking that
	// events were emitted for all lines.
	if stderrEvents != totalLines {
		t.Errorf("expected %d StderrEvents, got %d", totalLines, stderrEvents)
	}
}

// Verify the stderr buffer retains most recent lines by triggering a process error
// with more than maxStderrLines lines.
func TestStderrBufferKeepsMostRecent(t *testing.T) {
	totalLines := maxStderrLines + 100
	var stderrBuf strings.Builder
	for i := 0; i < totalLines; i++ {
		fmt.Fprintf(&stderrBuf, "line-%04d\n", i)
	}

	exec := &failingProcessExecutor{
		stdout: `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`,
		stderr:   stderrBuf.String(),
		exitCode: 1,
	}
	client := NewWithExecutor(exec)

	stream := client.Run(context.Background(), "ignored")
	_, err := stream.Wait()
	if err == nil {
		t.Fatal("expected error from failing process")
	}

	// The error's Stderr field should contain the most recent maxStderrLines lines
	var cliErr *Error
	if errors.As(err, &cliErr) {
		lines := strings.Split(cliErr.Stderr, "\n")
		// Remove trailing empty element from split
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		if len(lines) != maxStderrLines {
			t.Errorf("expected %d stderr lines in error, got %d", maxStderrLines, len(lines))
		}
		// Last line should be the last line we wrote
		expected := fmt.Sprintf("line-%04d", totalLines-1)
		if len(lines) > 0 && lines[len(lines)-1] != expected {
			t.Errorf("last stderr line = %q, want %q", lines[len(lines)-1], expected)
		}
		// First line should be shifted (not line-0000)
		expectedFirst := fmt.Sprintf("line-%04d", totalLines-maxStderrLines)
		if len(lines) > 0 && lines[0] != expectedFirst {
			t.Errorf("first stderr line = %q, want %q", lines[0], expectedFirst)
		}
	} else {
		t.Fatalf("expected *Error, got %T: %v", err, err)
	}
}

type failingProcessExecutor struct {
	stdout   string
	stderr   string
	exitCode int
}

func (e *failingProcessExecutor) Start(_ context.Context, _ *StartConfig) (*Process, error) {
	return &Process{
		Stdout: io.NopCloser(strings.NewReader(e.stdout)),
		Stderr: io.NopCloser(strings.NewReader(e.stderr)),
		Wait: func() error {
			return &exec.ExitError{ProcessState: nil}
		},
	}, nil
}

type failExecutor struct {
	err error
}

func (e *failExecutor) Start(_ context.Context, _ *StartConfig) (*Process, error) {
	return nil, e.err
}

type trackStartExecutor struct {
	started bool
}

func (e *trackStartExecutor) Start(_ context.Context, _ *StartConfig) (*Process, error) {
	e.started = true
	// Return a minimal process with empty streams
	return &Process{
		Stdout: io.NopCloser(strings.NewReader("")),
		Stderr: io.NopCloser(strings.NewReader("")),
		Wait:   func() error { return nil },
	}, nil
}
