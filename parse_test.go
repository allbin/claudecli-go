package claudecli

import (
	"os"
	"strings"
	"testing"
)

func collectEvents(t *testing.T, path string) []Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		ParseEvents(f, ch)
	}()

	var events []Event
	for e := range ch {
		events = append(events, e)
	}
	return events
}

func TestParseBasicStream(t *testing.T) {
	events := collectEvents(t, "testdata/basic.jsonl")

	if len(events) == 0 {
		t.Fatal("no events parsed")
	}

	// First event should be Init
	if _, ok := events[0].(*InitEvent); !ok {
		t.Fatalf("expected InitEvent, got %T", events[0])
	}

	init := events[0].(*InitEvent)
	if init.SessionID == "" {
		t.Error("InitEvent missing session ID")
	}

	// Should have at least one TextEvent
	var gotText bool
	for _, e := range events {
		if te, ok := e.(*TextEvent); ok {
			gotText = true
			if te.Content == "" {
				t.Error("TextEvent has empty content")
			}
		}
	}
	if !gotText {
		t.Error("no TextEvent found")
	}

	// Should have a RateLimitEvent
	var gotRateLimit bool
	for _, e := range events {
		if _, ok := e.(*RateLimitEvent); ok {
			gotRateLimit = true
		}
	}
	if !gotRateLimit {
		t.Error("no RateLimitEvent found")
	}

	// Last non-error event should be Result
	last := events[len(events)-1]
	result, ok := last.(*ResultEvent)
	if !ok {
		t.Fatalf("expected ResultEvent last, got %T", last)
	}
	if result.Text == "" {
		t.Error("ResultEvent has empty text")
	}
	if result.CostUSD <= 0 {
		t.Error("ResultEvent has zero cost")
	}
	if result.SessionID == "" {
		t.Error("ResultEvent missing session ID")
	}
}

func TestParseToolUseStream(t *testing.T) {
	events := collectEvents(t, "testdata/tool_use.jsonl")

	if len(events) == 0 {
		t.Fatal("no events parsed")
	}

	var gotThinking, gotToolUse, gotToolResult, gotText bool
	for _, e := range events {
		switch ev := e.(type) {
		case *ThinkingEvent:
			gotThinking = true
			if ev.Content == "" {
				t.Error("ThinkingEvent has empty content")
			}
		case *ToolUseEvent:
			gotToolUse = true
			if ev.Name == "" {
				t.Error("ToolUseEvent has empty name")
			}
			if ev.ID == "" {
				t.Error("ToolUseEvent has empty ID")
			}
		case *ToolResultEvent:
			gotToolResult = true
			if ev.ToolUseID == "" {
				t.Error("ToolResultEvent has empty tool use ID")
			}
		case *TextEvent:
			gotText = true
		}
	}

	if !gotThinking {
		t.Error("no ThinkingEvent found")
	}
	if !gotToolUse {
		t.Error("no ToolUseEvent found")
	}
	if !gotToolResult {
		t.Error("no ToolResultEvent found")
	}
	if !gotText {
		t.Error("no TextEvent found")
	}
}

func TestParseMalformedJSONL(t *testing.T) {
	input := `{"type":"system","session_id":"test","model":"sonnet"}
not valid json
{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}
also broken {{{
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`

	ch := make(chan Event, 64)
	go func() {
		ParseEvents(strings.NewReader(input), ch)
		close(ch)
	}()

	var events []Event
	for e := range ch {
		events = append(events, e)
	}

	var errorCount, initCount, textCount, resultCount int
	for _, e := range events {
		switch e.(type) {
		case *ErrorEvent:
			errorCount++
		case *InitEvent:
			initCount++
		case *TextEvent:
			textCount++
		case *ResultEvent:
			resultCount++
		}
	}

	if errorCount != 2 {
		t.Errorf("expected 2 ErrorEvents for bad lines, got %d", errorCount)
	}
	if initCount != 1 {
		t.Errorf("expected 1 InitEvent, got %d", initCount)
	}
	if textCount != 1 {
		t.Errorf("expected 1 TextEvent, got %d", textCount)
	}
	if resultCount != 1 {
		t.Errorf("expected 1 ResultEvent, got %d", resultCount)
	}
}

func TestParseReturnsAfterResult(t *testing.T) {
	// Simulate a CLI that keeps stdout open after result (known bug).
	// ParseEvents should return after the result event without blocking.
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
{"type":"assistant","message":{"content":[{"type":"text","text":"should not appear"}]}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(strings.NewReader(input), ch)
		close(ch)
	}()

	var events []Event
	for e := range ch {
		events = append(events, e)
	}

	// Should get init, text, result — but NOT the text after result
	for _, e := range events {
		if te, ok := e.(*TextEvent); ok && te.Content == "should not appear" {
			t.Error("ParseEvents continued reading after result event")
		}
	}
	var gotResult bool
	for _, e := range events {
		if _, ok := e.(*ResultEvent); ok {
			gotResult = true
		}
	}
	if !gotResult {
		t.Error("missing ResultEvent")
	}
}
