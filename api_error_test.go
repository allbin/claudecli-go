package claudecli

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// testdata/prompt_too_long.jsonl is a capture from Claude Code 2.1.283 given
// ~1.6 MB of piped text: init, a synthetic assistant message carrying
// is_api_error_message (snake_case) and "error":"invalid_request", then a
// result with is_error, terminal_reason "prompt_too_long" and
// api_error_status 400. The process exits 1 with empty stderr.

func TestParsePromptTooLongCapture(t *testing.T) {
	events := collectEvents(t, "testdata/prompt_too_long.jsonl")

	var fatal *ErrorEvent
	for _, e := range events {
		switch ev := e.(type) {
		case *TextEvent:
			t.Errorf("synthetic error text leaked as TextEvent: %q", ev.Content)
		case *ErrorEvent:
			if ev.Fatal {
				fatal = ev
			}
		}
	}
	if fatal == nil {
		t.Fatal("no fatal ErrorEvent for the synthetic prompt-too-long message")
	}
	if !errors.Is(fatal.Err, ErrContextWindowExceeded) {
		t.Errorf("fatal error = %v, want ErrContextWindowExceeded", fatal.Err)
	}
	if !errors.Is(fatal.Err, ErrAPI) {
		t.Errorf("fatal error = %v, want ErrAPI kept for synthetic messages", fatal.Err)
	}
	if !strings.Contains(fatal.Err.Error(), "Prompt is too long") {
		t.Errorf("fatal error = %q, want the CLI's text", fatal.Err.Error())
	}
}

func TestParseSyntheticAPIErrorClass(t *testing.T) {
	synthetic := func(text, extra string) string {
		return `{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"` + text + `"}]},"parent_tool_use_id":null` + extra + `}`
	}
	tests := []struct {
		name string
		line string
		want error
	}{
		{"old CLI camelCase short text", synthetic("Prompt is too long", `,"isApiErrorMessage":true`), ErrContextWindowExceeded},
		{"rate_limit", synthetic("API Error: Rate limit reached", `,"error":"rate_limit","is_api_error_message":true`), ErrRateLimit},
		{"authentication_failed", synthetic("Invalid API key", `,"error":"authentication_failed","is_api_error_message":true`), ErrAuth},
		{"overloaded", synthetic("API Error: Overloaded", `,"error":"overloaded","is_api_error_message":true`), ErrOverloaded},
		{"billing_error", synthetic("Credit balance is too low", `,"error":"billing_error","is_api_error_message":true`), ErrBilling},
		{"model_not_found", synthetic("model not found", `,"error":"model_not_found","is_api_error_message":true`), ErrNotFound},
		{"status only", synthetic("API Error: 429", `,"api_error_status":429,"is_api_error_message":true`), ErrRateLimit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := parseLines(t, `{"type":"system","subtype":"init","session_id":"s"}`, tt.line)
			var fatal error
			for _, e := range events {
				if ev, ok := e.(*ErrorEvent); ok && ev.Fatal {
					fatal = ev.Err
				}
				if ev, ok := e.(*TextEvent); ok {
					t.Errorf("synthetic text leaked: %q", ev.Content)
				}
			}
			if !errors.Is(fatal, tt.want) {
				t.Errorf("fatal = %v, want %v", fatal, tt.want)
			}
			if !errors.Is(fatal, ErrAPI) {
				t.Errorf("fatal = %v, want ErrAPI kept", fatal)
			}
		})
	}
}

func TestParseSyntheticUnknownClassIsPlainAPIError(t *testing.T) {
	events := parseLines(t,
		`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error: socket closed"}]},"error":"unknown","is_api_error_message":true}`)
	var fatal error
	for _, e := range events {
		if ev, ok := e.(*ErrorEvent); ok && ev.Fatal {
			fatal = ev.Err
		}
	}
	if !errors.Is(fatal, ErrAPI) {
		t.Fatalf("fatal = %v, want ErrAPI", fatal)
	}
	if got := fatal.Error(); got != "API error: API Error: socket closed" {
		t.Errorf("fatal = %q", got)
	}
}

// A subagent's API error ends the subagent, not the run: the main agent gets
// a tool result and carries on.
func TestParseSubagentSyntheticAPIErrorNotFatal(t *testing.T) {
	events := parseLines(t,
		`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error: Overloaded"}]},"parent_tool_use_id":"toolu_1","error":"overloaded","is_api_error_message":true}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]},"parent_tool_use_id":null}`,
		`{"type":"result","subtype":"success","result":"done"}`)
	var sawErr, sawResult bool
	for _, e := range events {
		switch ev := e.(type) {
		case *ErrorEvent:
			if ev.Fatal {
				t.Errorf("subagent API error must not be fatal: %v", ev.Err)
			}
			if errors.Is(ev.Err, ErrOverloaded) {
				sawErr = true
			}
		case *TextEvent:
			if ev.ParentToolUseID != "" {
				t.Errorf("subagent synthetic text leaked: %q", ev.Content)
			}
		case *ResultEvent:
			sawResult = true
		}
	}
	if !sawErr {
		t.Error("no classified ErrorEvent for the subagent API error")
	}
	if !sawResult {
		t.Error("stream stopped at the subagent API error")
	}
}

func TestParseErrorResultClassified(t *testing.T) {
	tests := []struct {
		name   string
		result string
		want   error // nil: no ErrorEvent
	}{
		{"prompt_too_long", `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"prompt_too_long","api_error_status":400,"result":"Prompt is too long"}`, ErrContextWindowExceeded},
		{"blocking_limit", `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"blocking_limit","result":"Prompt is too long · automatic compaction failed: x"}`, ErrContextWindowExceeded},
		{"status 429", `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":429,"result":"API Error: rate limited"}`, ErrRateLimit},
		{"status 529", `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":529,"result":"API Error: overloaded"}`, ErrOverloaded},
		{"status 500", `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":500,"result":"API Error: internal"}`, ErrAPI},
		{"aborted", `{"type":"result","subtype":"error_during_execution","is_error":true,"terminal_reason":"aborted_streaming"}`, nil},
		{"not an error", `{"type":"result","subtype":"success","result":"Prompt is too long, said the model","terminal_reason":"completed"}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := parseLines(t, tt.result)
			var errEv *ErrorEvent
			var res *ResultEvent
			for _, e := range events {
				switch ev := e.(type) {
				case *ErrorEvent:
					errEv = ev
				case *ResultEvent:
					res = ev
				}
			}
			if res == nil {
				t.Fatal("no ResultEvent")
			}
			if tt.want == nil {
				if errEv != nil {
					t.Errorf("unexpected ErrorEvent: %v", errEv.Err)
				}
				return
			}
			if errEv == nil {
				t.Fatal("no ErrorEvent for is_error result")
			}
			if errEv.Fatal {
				t.Error("result classification must be non-fatal; the process exit decides")
			}
			if !errors.Is(errEv.Err, tt.want) {
				t.Errorf("err = %v, want %v", errEv.Err, tt.want)
			}
		})
	}
}

func TestResultEventErrorFields(t *testing.T) {
	events := parseLines(t, `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"prompt_too_long","api_error_status":400,"result":"Prompt is too long"}`)
	var res *ResultEvent
	for _, e := range events {
		if r, ok := e.(*ResultEvent); ok {
			res = r
		}
	}
	if res == nil {
		t.Fatal("no ResultEvent")
	}
	if !res.IsError || res.TerminalReason != "prompt_too_long" || res.APIErrorStatus != 400 {
		t.Errorf("IsError=%v TerminalReason=%q APIErrorStatus=%d", res.IsError, res.TerminalReason, res.APIErrorStatus)
	}
}

func TestRunPromptTooLongExitError(t *testing.T) {
	stdout, err := os.ReadFile("testdata/prompt_too_long.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	client := NewWithExecutor(&failingProcessExecutor{stdout: string(stdout), exitCode: 1})
	stream := client.Run(context.Background(), "ignored")

	var last *ErrorEvent
	for ev := range stream.Events() {
		if e, ok := ev.(*ErrorEvent); ok && e.Fatal {
			last = e
		}
	}
	if last == nil {
		t.Fatal("no fatal ErrorEvent")
	}
	var cliErr *Error
	if !errors.As(last.Err, &cliErr) {
		t.Fatalf("final fatal error = %T %v, want *Error", last.Err, last.Err)
	}
	if !errors.Is(cliErr, ErrContextWindowExceeded) {
		t.Errorf("process exit error = %v, want ErrContextWindowExceeded", cliErr)
	}
	if !strings.Contains(cliErr.Message, "Prompt is too long") {
		t.Errorf("Message = %q", cliErr.Message)
	}
	if _, err := stream.Wait(); !errors.Is(err, ErrContextWindowExceeded) {
		t.Errorf("Wait = %v, want ErrContextWindowExceeded", err)
	}
}

// An is_error result nothing classifies still names the failure on exit.
func TestRunUnclassifiedErrorResultExitMessage(t *testing.T) {
	client := NewWithExecutor(&failingProcessExecutor{
		stdout: `{"type":"system","subtype":"init","session_id":"s"}
{"type":"result","subtype":"error_during_execution","is_error":true,"terminal_reason":"turn_setup_failed","errors":["queryParams builder failed: boom"]}
`,
		exitCode: 1,
	})
	_, err := client.Run(context.Background(), "ignored").Wait()
	var cliErr *Error
	if !errors.As(err, &cliErr) {
		t.Fatalf("err = %T %v, want *Error", err, err)
	}
	if !strings.Contains(cliErr.Message, "queryParams builder failed: boom") || !strings.Contains(cliErr.Message, "turn_setup_failed") {
		t.Errorf("Message = %q", cliErr.Message)
	}
}

func TestSessionSnakeCaseSyntheticPromptTooLong(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)
	capture, err := os.ReadFile("testdata/prompt_too_long.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(capture)), "\n")

	go func() {
		sim.handleInitAndReady(t)
		sim.readStdin(t)
		sim.send(lines[1])
		sim.send(lines[2])
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = sim.bidi.StdoutWriter.Close()
		_ = session.Close()
	}()
	if err := session.Query("big"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-session.Events():
			switch e := ev.(type) {
			case *TextEvent:
				t.Fatalf("synthetic text leaked: %q", e.Content)
			case *ErrorEvent:
				if !e.Fatal {
					t.Fatalf("unexpected non-fatal ErrorEvent before the fatal one: %v", e.Err)
				}
				if !errors.Is(e.Err, ErrContextWindowExceeded) {
					t.Errorf("fatal = %v, want ErrContextWindowExceeded", e.Err)
				}
				if _, err := session.Wait(); !errors.Is(err, ErrContextWindowExceeded) {
					t.Errorf("Wait = %v, want ErrContextWindowExceeded", err)
				}
				return
			}
		case <-deadline:
			t.Fatal("timeout: no fatal ErrorEvent")
		}
	}
}

// exitingBidiExecutor is a BidiFixtureExecutor whose process exits with
// waitErr.
type exitingBidiExecutor struct {
	*BidiFixtureExecutor
	waitErr error
}

func (e exitingBidiExecutor) Start(ctx context.Context, cfg *StartConfig) (*Process, error) {
	p, err := e.BidiFixtureExecutor.Start(ctx, cfg)
	if err != nil {
		return nil, err
	}
	p.Wait = func() error { return e.waitErr }
	return p, nil
}

// Without a synthetic message, a classified is_error result reaches the
// session's process-exit error.
func TestSessionErrorResultClassifiesExit(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(exitingBidiExecutor{sim.bidi, mustExitErr(t, "false")})

	go func() {
		sim.handleInitAndReady(t)
		sim.readStdin(t)
		sim.send(`{"type":"result","subtype":"success","is_error":true,"terminal_reason":"prompt_too_long","api_error_status":400,"result":"Prompt is too long"}`)
		_ = sim.bidi.StdoutWriter.Close()
		_, _ = io.Copy(io.Discard, sim.reader)
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	if err := session.Query("big"); err != nil {
		t.Fatal(err)
	}
	var sawNonFatal bool
	var exitErr *Error
	deadline := time.After(2 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-session.Events():
			if !ok {
				break loop
			}
			e, isErr := ev.(*ErrorEvent)
			if !isErr {
				continue
			}
			if !e.Fatal && errors.Is(e.Err, ErrContextWindowExceeded) {
				sawNonFatal = true
			}
			if e.Fatal && errors.As(e.Err, &exitErr) {
				break loop
			}
		case <-deadline:
			t.Fatal("timeout waiting for process exit error")
		}
	}
	if !sawNonFatal {
		t.Error("no non-fatal ErrorEvent for the is_error result")
	}
	if exitErr == nil {
		t.Fatal("no fatal *Error on process exit")
	}
	if !errors.Is(exitErr, ErrContextWindowExceeded) {
		t.Errorf("exit error = %v, want ErrContextWindowExceeded", exitErr)
	}
}
