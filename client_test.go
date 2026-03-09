package claudecli

import (
	"context"
	"errors"
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

type failExecutor struct {
	err error
}

func (e *failExecutor) Start(_ context.Context, _ *StartConfig) (*Process, error) {
	return nil, e.err
}
