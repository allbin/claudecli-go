package claudecli

import (
	"encoding/json"
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
	if len(result.ModelUsage) == 0 {
		t.Fatal("ResultEvent has no ModelUsage")
	}
	mu, ok := result.ModelUsage["claude-haiku-4-5-20251001"]
	if !ok {
		t.Fatal("ModelUsage missing claude-haiku-4-5-20251001 entry")
	}
	if mu.ContextWindow != 200000 {
		t.Errorf("ContextWindow = %d, want 200000", mu.ContextWindow)
	}
	if mu.MaxOutputTokens != 32000 {
		t.Errorf("MaxOutputTokens = %d, want 32000", mu.MaxOutputTokens)
	}
	if mu.InputTokens != 9 {
		t.Errorf("InputTokens = %d, want 9", mu.InputTokens)
	}
	if mu.OutputTokens != 997 {
		t.Errorf("OutputTokens = %d, want 997", mu.OutputTokens)
	}
	if mu.CostUSD <= 0 {
		t.Error("ModelUsage CostUSD is zero")
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

func TestParseToolResultArrayContent(t *testing.T) {
	// MCP tool results send content as an array of content blocks.
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"assistant","message":{"content":[{"type":"tool_result","tool_use_id":"tu_123","content":[{"type":"text","text":"mcp result text"}]}]}}
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

	var gotToolResult bool
	for _, e := range events {
		if tr, ok := e.(*ToolResultEvent); ok {
			gotToolResult = true
			if tr.Text() != "mcp result text" {
				t.Errorf("expected 'mcp result text', got %q", tr.Text())
			}
			if tr.ToolUseID != "tu_123" {
				t.Errorf("expected tool_use_id 'tu_123', got %q", tr.ToolUseID)
			}
			if len(tr.Content) != 1 || tr.Content[0].Type != "text" {
				t.Errorf("expected 1 text block, got %v", tr.Content)
			}
		}
	}
	if !gotToolResult {
		t.Error("no ToolResultEvent found")
	}

	// Verify no errors were emitted.
	for _, e := range events {
		if err, ok := e.(*ErrorEvent); ok {
			t.Errorf("unexpected error: %v", err)
		}
	}
}

func TestParseToolResultStringContent(t *testing.T) {
	// Regular tool results send content as a plain string.
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"assistant","message":{"content":[{"type":"tool_result","tool_use_id":"tu_456","content":"plain string result"}]}}
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

	var gotToolResult bool
	for _, e := range events {
		if tr, ok := e.(*ToolResultEvent); ok {
			gotToolResult = true
			if tr.Text() != "plain string result" {
				t.Errorf("expected 'plain string result', got %q", tr.Text())
			}
			if len(tr.Content) != 1 || tr.Content[0].Type != "text" {
				t.Errorf("expected 1 text block, got %v", tr.Content)
			}
		}
	}
	if !gotToolResult {
		t.Error("no ToolResultEvent found")
	}

	for _, e := range events {
		if err, ok := e.(*ErrorEvent); ok {
			t.Errorf("unexpected error: %v", err)
		}
	}
}

func TestParseToolResultMixedContent(t *testing.T) {
	// Tool result with both text and image content blocks (e.g. Playwright screenshot).
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"assistant","message":{"content":[{"type":"tool_result","tool_use_id":"tu_789","content":[{"type":"text","text":"Screenshot taken"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]}]}}
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

	var tr *ToolResultEvent
	for _, e := range events {
		if r, ok := e.(*ToolResultEvent); ok {
			tr = r
		}
	}
	if tr == nil {
		t.Fatal("no ToolResultEvent found")
	}
	if tr.ToolUseID != "tu_789" {
		t.Errorf("ToolUseID = %q, want tu_789", tr.ToolUseID)
	}
	if len(tr.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(tr.Content))
	}

	// First block: text
	if tr.Content[0].Type != "text" {
		t.Errorf("block[0].Type = %q, want text", tr.Content[0].Type)
	}
	if tr.Content[0].Text != "Screenshot taken" {
		t.Errorf("block[0].Text = %q", tr.Content[0].Text)
	}

	// Second block: image
	if tr.Content[1].Type != "image" {
		t.Errorf("block[1].Type = %q, want image", tr.Content[1].Type)
	}
	if tr.Content[1].MediaType != "image/png" {
		t.Errorf("block[1].MediaType = %q", tr.Content[1].MediaType)
	}
	if tr.Content[1].Data != "iVBORw0KGgo=" {
		t.Errorf("block[1].Data = %q", tr.Content[1].Data)
	}

	// Text() should return only text blocks.
	if tr.Text() != "Screenshot taken" {
		t.Errorf("Text() = %q, want 'Screenshot taken'", tr.Text())
	}

	for _, e := range events {
		if err, ok := e.(*ErrorEvent); ok {
			t.Errorf("unexpected error: %v", err)
		}
	}
}

func TestParseControlRequest(t *testing.T) {
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"control_request","request_id":"req_1","request":{"subtype":"can_use_tool","tool_name":"Bash"}}
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

	var gotControl bool
	for _, e := range events {
		if cr, ok := e.(*ControlRequestEvent); ok {
			gotControl = true
			if cr.RequestID != "req_1" {
				t.Errorf("expected request_id 'req_1', got %q", cr.RequestID)
			}
			if cr.Subtype != "can_use_tool" {
				t.Errorf("expected subtype 'can_use_tool', got %q", cr.Subtype)
			}
		}
	}
	if !gotControl {
		t.Error("no ControlRequestEvent found")
	}
}

func TestParseStreamEvent(t *testing.T) {
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"stream_event","uuid":"abc-123","session_id":"test","event":{"type":"content_block_delta"}}
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

	var gotStream bool
	for _, e := range events {
		if se, ok := e.(*StreamEvent); ok {
			gotStream = true
			if se.UUID != "abc-123" {
				t.Errorf("expected uuid 'abc-123', got %q", se.UUID)
			}
		}
	}
	if !gotStream {
		t.Error("no StreamEvent found")
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

func TestParseResultStopReason(t *testing.T) {
	input := `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}
{"type":"result","subtype":"success","stop_reason":"end_turn","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
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

	var result *ResultEvent
	for _, e := range events {
		if r, ok := e.(*ResultEvent); ok {
			result = r
		}
	}
	if result == nil {
		t.Fatal("no ResultEvent found")
	}
	if result.StopReason != "end_turn" {
		t.Errorf("expected stop_reason 'end_turn', got %q", result.StopReason)
	}
	if !strings.Contains(result.String(), "StopReason: end_turn") {
		t.Error("String() should include StopReason when set")
	}
}

func TestParseResultStructuredOutput(t *testing.T) {
	input := `{"type":"result","subtype":"success","stop_reason":"end_turn","structured_output":{"name":"test","value":42},"total_cost_usd":0.02,"usage":{"input_tokens":20,"output_tokens":10}}
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

	var result *ResultEvent
	for _, e := range events {
		if r, ok := e.(*ResultEvent); ok {
			result = r
		}
	}
	if result == nil {
		t.Fatal("no ResultEvent found")
	}
	if result.StructuredOutput == nil {
		t.Fatal("expected non-nil StructuredOutput")
	}
	var parsed map[string]any
	if err := json.Unmarshal(result.StructuredOutput, &parsed); err != nil {
		t.Fatalf("failed to unmarshal StructuredOutput: %v", err)
	}
	if parsed["name"] != "test" {
		t.Errorf("expected name 'test', got %v", parsed["name"])
	}
}

// Fix #2: RateLimitEvent reads from nested rate_limit_info JSON.
func TestParseRateLimitEventNestedFields(t *testing.T) {
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","utilization":0.82},"uuid":"abc-123","session_id":"test"}
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

	var rle *RateLimitEvent
	for _, e := range events {
		if r, ok := e.(*RateLimitEvent); ok {
			rle = r
		}
	}
	if rle == nil {
		t.Fatal("no RateLimitEvent found")
	}
	if rle.Status != "allowed_warning" {
		t.Errorf("Status = %q, want 'allowed_warning'", rle.Status)
	}
	if rle.Utilization != 0.82 {
		t.Errorf("Utilization = %f, want 0.82", rle.Utilization)
	}
	if rle.UUID != "abc-123" {
		t.Errorf("UUID = %q, want 'abc-123'", rle.UUID)
	}
	if rle.SessionID != "test" {
		t.Errorf("SessionID = %q, want 'test'", rle.SessionID)
	}
}

// Verify rate_limit_event with all fields parses correctly.
func TestParseRateLimitEventAllFields(t *testing.T) {
	input := `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1772773200,"rateLimitType":"seven_day","utilization":0.95,"overageStatus":"rejected","overageResetsAt":1772780000,"overageDisabledReason":"out_of_credits","isUsingOverage":false,"surpassedThreshold":0.75},"uuid":"def-456","session_id":"sess-1"}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(strings.NewReader(input), ch)
		close(ch)
	}()

	var rle *RateLimitEvent
	for e := range ch {
		if r, ok := e.(*RateLimitEvent); ok {
			rle = r
		}
	}
	if rle == nil {
		t.Fatal("no RateLimitEvent found")
	}
	if rle.Status != "rejected" {
		t.Errorf("Status = %q", rle.Status)
	}
	if rle.Utilization != 0.95 {
		t.Errorf("Utilization = %f", rle.Utilization)
	}
	if rle.ResetsAt != 1772773200 {
		t.Errorf("ResetsAt = %d, want 1772773200", rle.ResetsAt)
	}
	if rle.RateLimitType != "seven_day" {
		t.Errorf("RateLimitType = %q", rle.RateLimitType)
	}
	if rle.OverageStatus != "rejected" {
		t.Errorf("OverageStatus = %q", rle.OverageStatus)
	}
	if rle.OverageResetsAt != 1772780000 {
		t.Errorf("OverageResetsAt = %d", rle.OverageResetsAt)
	}
	if rle.OverageDisabledReason != "out_of_credits" {
		t.Errorf("OverageDisabledReason = %q", rle.OverageDisabledReason)
	}
	if rle.UUID != "def-456" {
		t.Errorf("UUID = %q", rle.UUID)
	}
	if rle.SessionID != "sess-1" {
		t.Errorf("SessionID = %q", rle.SessionID)
	}
	// Raw map should include unmodeled fields
	if rle.Raw["isUsingOverage"] != false {
		t.Errorf("Raw[isUsingOverage] = %v", rle.Raw["isUsingOverage"])
	}
	if rle.Raw["surpassedThreshold"] != 0.75 {
		t.Errorf("Raw[surpassedThreshold] = %v", rle.Raw["surpassedThreshold"])
	}
}

// Minimal rate_limit_event — only status required.
func TestParseRateLimitEventMinimal(t *testing.T) {
	input := `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"},"uuid":"u","session_id":"s"}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(strings.NewReader(input), ch)
		close(ch)
	}()

	var rle *RateLimitEvent
	for e := range ch {
		if r, ok := e.(*RateLimitEvent); ok {
			rle = r
		}
	}
	if rle == nil {
		t.Fatal("no RateLimitEvent found")
	}
	if rle.Status != "allowed" {
		t.Errorf("Status = %q", rle.Status)
	}
	if rle.ResetsAt != 0 {
		t.Errorf("ResetsAt should be 0 when absent, got %d", rle.ResetsAt)
	}
	if rle.RateLimitType != "" {
		t.Errorf("RateLimitType should be empty when absent, got %q", rle.RateLimitType)
	}
}

func TestParseThinkingSignature(t *testing.T) {
	input := `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"let me think","signature":"sig_abc123"}]}}
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

	var thinking *ThinkingEvent
	for _, e := range events {
		if te, ok := e.(*ThinkingEvent); ok {
			thinking = te
		}
	}
	if thinking == nil {
		t.Fatal("no ThinkingEvent found")
	}
	if thinking.Content != "let me think" {
		t.Errorf("expected content 'let me think', got %q", thinking.Content)
	}
	if thinking.Signature != "sig_abc123" {
		t.Errorf("expected signature 'sig_abc123', got %q", thinking.Signature)
	}
}
