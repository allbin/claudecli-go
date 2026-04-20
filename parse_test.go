package claudecli

import (
	"context"
	"encoding/json"
	"errors"
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
		ParseEvents(context.Background(),f, ch)
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
	if mu.WebSearchRequests != 0 {
		t.Errorf("WebSearchRequests = %d, want 0", mu.WebSearchRequests)
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
		ParseEvents(context.Background(),strings.NewReader(input), ch)
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
		ParseEvents(context.Background(),strings.NewReader(input), ch)
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
		ParseEvents(context.Background(),strings.NewReader(input), ch)
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
		ParseEvents(context.Background(),strings.NewReader(input), ch)
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
		ParseEvents(context.Background(),strings.NewReader(input), ch)
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
		ParseEvents(context.Background(),strings.NewReader(input), ch)
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
		ParseEvents(context.Background(),strings.NewReader(input), ch)
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
		ParseEvents(context.Background(),strings.NewReader(input), ch)
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
		ParseEvents(context.Background(),strings.NewReader(input), ch)
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
		ParseEvents(context.Background(),strings.NewReader(input), ch)
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
		ParseEvents(context.Background(),strings.NewReader(input), ch)
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
		ParseEvents(context.Background(),strings.NewReader(input), ch)
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
		ParseEvents(context.Background(),strings.NewReader(input), ch)
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

func TestParseCompactStatusEvent(t *testing.T) {
	input := `{"type":"system","subtype":"init","session_id":"test","model":"sonnet"}
{"type":"system","subtype":"status","status":"compacting","session_id":"test"}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var cs *CompactStatusEvent
	for e := range ch {
		if c, ok := e.(*CompactStatusEvent); ok {
			cs = c
		}
	}
	if cs == nil {
		t.Fatal("no CompactStatusEvent found")
	}
	if cs.Status != "compacting" {
		t.Errorf("expected status 'compacting', got %q", cs.Status)
	}
	if cs.SessionID != "test" {
		t.Errorf("expected session_id 'test', got %q", cs.SessionID)
	}
}

func TestParseCompactStatusNull(t *testing.T) {
	input := `{"type":"system","subtype":"init","session_id":"test","model":"sonnet"}
{"type":"system","subtype":"status","status":null,"session_id":"test"}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var cs *CompactStatusEvent
	for e := range ch {
		if c, ok := e.(*CompactStatusEvent); ok {
			cs = c
		}
	}
	if cs == nil {
		t.Fatal("no CompactStatusEvent found for null status")
	}
	if cs.Status != "" {
		t.Errorf("expected empty status for null, got %q", cs.Status)
	}
}

func TestParseCompactBoundaryEvent(t *testing.T) {
	input := `{"type":"system","subtype":"init","session_id":"test","model":"sonnet"}
{"type":"system","subtype":"compact_boundary","session_id":"test","compact_metadata":{"trigger":"manual","pre_tokens":19030}}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var cb *CompactBoundaryEvent
	for e := range ch {
		if c, ok := e.(*CompactBoundaryEvent); ok {
			cb = c
		}
	}
	if cb == nil {
		t.Fatal("no CompactBoundaryEvent found")
	}
	if cb.Trigger != "manual" {
		t.Errorf("expected trigger 'manual', got %q", cb.Trigger)
	}
	if cb.PreTokens != 19030 {
		t.Errorf("expected pre_tokens 19030, got %d", cb.PreTokens)
	}
	if cb.SessionID != "test" {
		t.Errorf("expected session_id 'test', got %q", cb.SessionID)
	}
	if len(cb.Raw) == 0 {
		t.Error("CompactBoundaryEvent has empty Raw")
	}
}

func TestParseSystemInitSubtype(t *testing.T) {
	// Explicit subtype:"init" should still emit InitEvent.
	input := `{"type":"system","subtype":"init","session_id":"test","model":"sonnet","tools":["Bash"]}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var init *InitEvent
	for e := range ch {
		if i, ok := e.(*InitEvent); ok {
			init = i
		}
	}
	if init == nil {
		t.Fatal("no InitEvent found for subtype 'init'")
	}
	if init.SessionID != "test" {
		t.Errorf("expected session_id 'test', got %q", init.SessionID)
	}
}

func TestParseSystemNoSubtype(t *testing.T) {
	// Old-style system event without subtype should still emit InitEvent.
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var init *InitEvent
	for e := range ch {
		if i, ok := e.(*InitEvent); ok {
			init = i
		}
	}
	if init == nil {
		t.Fatal("no InitEvent found for missing subtype")
	}
}

func TestParseCompactionSequence(t *testing.T) {
	// Full compaction sequence as observed from real CLI.
	input := `{"type":"system","subtype":"init","session_id":"s1","model":"sonnet"}
{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}],"context_management":null}}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
{"type":"system","subtype":"status","status":"compacting","session_id":"s1"}
{"type":"system","subtype":"status","status":null,"session_id":"s1"}
{"type":"system","subtype":"init","session_id":"s1","model":"sonnet"}
{"type":"system","subtype":"compact_boundary","session_id":"s1","compact_metadata":{"trigger":"manual","pre_tokens":19030}}
{"type":"user","message":{"role":"user","content":"summary..."}}
{"type":"result","subtype":"success","total_cost_usd":0.02,"usage":{"input_tokens":20,"output_tokens":10}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var events []Event
	for e := range ch {
		events = append(events, e)
	}

	// Expected: InitEvent, TurnEvent, TextEvent, ResultEvent (parser returns after first result).
	// The compaction events come after the first result, which terminates ParseEvents.
	// In session mode (readLoop) they would all be received.
	if len(events) < 4 {
		t.Fatalf("expected at least 4 events, got %d", len(events))
	}
	if _, ok := events[0].(*InitEvent); !ok {
		t.Errorf("event[0]: expected InitEvent, got %T", events[0])
	}
	if _, ok := events[1].(*TurnEvent); !ok {
		t.Errorf("event[1]: expected TurnEvent, got %T", events[1])
	}
	if _, ok := events[2].(*TextEvent); !ok {
		t.Errorf("event[2]: expected TextEvent, got %T", events[2])
	}
	if _, ok := events[3].(*ResultEvent); !ok {
		t.Errorf("event[3]: expected ResultEvent, got %T", events[3])
	}
}

func TestParseUnknownSystemSubtype(t *testing.T) {
	input := `{"type":"system","subtype":"init","session_id":"test","model":"sonnet"}
{"type":"system","subtype":"future_thing","session_id":"test"}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var unknown *UnknownEvent
	for e := range ch {
		switch e.(type) {
		case *CompactStatusEvent, *CompactBoundaryEvent:
			t.Errorf("unexpected compact event for unknown subtype: %T", e)
		}
		if u, ok := e.(*UnknownEvent); ok {
			unknown = u
		}
	}
	if unknown == nil {
		t.Fatal("expected UnknownEvent for unknown system subtype")
	}
	if unknown.Type != "system/future_thing" {
		t.Errorf("Type = %q, want %q", unknown.Type, "system/future_thing")
	}
}

func TestParseContextManagementEvent(t *testing.T) {
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}],"context_management":{"type":"summarized","summary":"prior conversation summary","tokens_before":180000,"tokens_after":120000}}}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var events []Event
	for e := range ch {
		events = append(events, e)
	}

	var cm *ContextManagementEvent
	for _, e := range events {
		if c, ok := e.(*ContextManagementEvent); ok {
			cm = c
		}
	}
	if cm == nil {
		t.Fatal("no ContextManagementEvent found")
	}
	if len(cm.Raw) == 0 {
		t.Error("ContextManagementEvent has empty Raw")
	}
	// Verify the raw JSON is parseable and contains expected fields.
	var parsed map[string]any
	if err := json.Unmarshal(cm.Raw, &parsed); err != nil {
		t.Fatalf("failed to unmarshal Raw: %v", err)
	}
	if parsed["type"] != "summarized" {
		t.Errorf("expected type 'summarized', got %v", parsed["type"])
	}
}

func TestParseContextManagementNull(t *testing.T) {
	// context_management: null should NOT emit an event.
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}],"context_management":null}}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	for e := range ch {
		if _, ok := e.(*ContextManagementEvent); ok {
			t.Error("ContextManagementEvent should not be emitted for null context_management")
		}
	}
}

func TestParseContextManagementAbsent(t *testing.T) {
	// No context_management field at all should NOT emit an event.
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	for e := range ch {
		if _, ok := e.(*ContextManagementEvent); ok {
			t.Error("ContextManagementEvent should not be emitted when field is absent")
		}
	}
}

func TestParseErrorEvent(t *testing.T) {
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"error","error":{"type":"api_error","message":"Internal server error"}}
{"type":"result","subtype":"error","total_cost_usd":0.001,"usage":{"input_tokens":10,"output_tokens":0}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var events []Event
	for e := range ch {
		events = append(events, e)
	}

	var gotError bool
	for _, e := range events {
		if ee, ok := e.(*ErrorEvent); ok {
			gotError = true
			if ee.Fatal {
				t.Error("error event should be non-fatal")
			}
			if !errors.Is(ee.Err, ErrAPI) {
				t.Errorf("expected errors.Is(_, ErrAPI), got %v", ee.Err)
			}
			if !strings.Contains(ee.Err.Error(), "Internal server error") {
				t.Errorf("error message missing, got %q", ee.Err.Error())
			}
		}
	}
	if !gotError {
		t.Error("no ErrorEvent found for type:error event")
	}
}

func TestParseErrorEventClassified(t *testing.T) {
	tests := []struct {
		name    string
		errType string
		target  error
	}{
		{"rate_limit", "rate_limit_error", ErrRateLimit},
		{"overloaded", "overloaded_error", ErrOverloaded},
		{"auth", "authentication_error", ErrAuth},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := `{"type":"error","error":{"type":"` + tt.errType + `","message":"test"}}
{"type":"result","subtype":"error","total_cost_usd":0,"usage":{"input_tokens":0,"output_tokens":0}}
`
			ch := make(chan Event, 64)
			go func() {
				ParseEvents(context.Background(),strings.NewReader(input), ch)
				close(ch)
			}()

			var found bool
			for e := range ch {
				if ee, ok := e.(*ErrorEvent); ok {
					found = true
					if !errors.Is(ee.Err, tt.target) {
						t.Errorf("expected errors.Is(_, %v), got %v", tt.target, ee.Err)
					}
					if ee.Fatal {
						t.Error("classified error event should be non-fatal")
					}
				}
			}
			if !found {
				t.Error("no ErrorEvent found")
			}
		})
	}
}

func TestParseErrorEventMinimal(t *testing.T) {
	// Empty error object and missing error field should not panic.
	inputs := []string{
		`{"type":"error","error":{}}` + "\n" + `{"type":"result","subtype":"error","total_cost_usd":0,"usage":{"input_tokens":0,"output_tokens":0}}`,
		`{"type":"error"}` + "\n" + `{"type":"result","subtype":"error","total_cost_usd":0,"usage":{"input_tokens":0,"output_tokens":0}}`,
	}
	for i, input := range inputs {
		ch := make(chan Event, 64)
		go func() {
			ParseEvents(context.Background(),strings.NewReader(input), ch)
			close(ch)
		}()

		var gotError bool
		for e := range ch {
			if ee, ok := e.(*ErrorEvent); ok {
				gotError = true
				if ee.Fatal {
					t.Errorf("case %d: expected non-fatal", i)
				}
				if ee.Err == nil {
					t.Errorf("case %d: Err should not be nil", i)
				}
			}
		}
		if !gotError {
			t.Errorf("case %d: no ErrorEvent found", i)
		}
	}
}

func TestParseErrorEventFixture(t *testing.T) {
	events := collectEvents(t, "testdata/error.jsonl")

	if len(events) == 0 {
		t.Fatal("no events parsed")
	}

	// Should have InitEvent, ErrorEvent, ResultEvent.
	if _, ok := events[0].(*InitEvent); !ok {
		t.Fatalf("expected InitEvent first, got %T", events[0])
	}

	var gotError bool
	for _, e := range events {
		if ee, ok := e.(*ErrorEvent); ok {
			gotError = true
			if !errors.Is(ee.Err, ErrAPI) {
				t.Errorf("expected ErrAPI, got %v", ee.Err)
			}
			if ee.Fatal {
				t.Error("expected non-fatal")
			}
		}
	}
	if !gotError {
		t.Error("no ErrorEvent from fixture")
	}

	// Last event should be ResultEvent.
	last := events[len(events)-1]
	if _, ok := last.(*ResultEvent); !ok {
		t.Fatalf("expected ResultEvent last, got %T", last)
	}
}

func TestContextSnapshotFromStreamEvents(t *testing.T) {
	input := `{"type":"system","session_id":"s1","model":"opus"}
{"type":"stream_event","uuid":"u1","session_id":"s1","event":{"type":"message_start","message":{"model":"claude-opus-4-20250514","usage":{"input_tokens":100,"cache_read_input_tokens":5000,"cache_creation_input_tokens":200}}}}
{"type":"stream_event","uuid":"u2","session_id":"s1","event":{"type":"content_block_delta","delta":{"text":"hi"}}}
{"type":"stream_event","uuid":"u3","session_id":"s1","event":{"type":"message_delta","usage":{"output_tokens":42}}}
{"type":"result","subtype":"success","total_cost_usd":0.05,"usage":{"input_tokens":100,"output_tokens":42,"cache_read_input_tokens":5000,"cache_creation_input_tokens":200},"modelUsage":{"claude-opus-4-20250514":{"inputTokens":100,"outputTokens":42,"cacheReadInputTokens":5000,"cacheCreationInputTokens":200,"contextWindow":200000}}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var result *ResultEvent
	for e := range ch {
		if r, ok := e.(*ResultEvent); ok {
			result = r
		}
	}
	if result == nil {
		t.Fatal("no ResultEvent")
	}
	cs := result.ContextSnapshot
	if cs == nil {
		t.Fatal("ContextSnapshot is nil")
	}
	if cs.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", cs.InputTokens)
	}
	if cs.CacheReadInputTokens != 5000 {
		t.Errorf("CacheReadInputTokens = %d, want 5000", cs.CacheReadInputTokens)
	}
	if cs.CacheCreationInputTokens != 200 {
		t.Errorf("CacheCreationInputTokens = %d, want 200", cs.CacheCreationInputTokens)
	}
	if cs.OutputTokens != 42 {
		t.Errorf("OutputTokens = %d, want 42", cs.OutputTokens)
	}
	if cs.ContextWindow != 200000 {
		t.Errorf("ContextWindow = %d, want 200000", cs.ContextWindow)
	}
}

func TestContextSnapshotNilWithoutStreamEvents(t *testing.T) {
	input := `{"type":"system","session_id":"s1","model":"opus"}
{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var result *ResultEvent
	for e := range ch {
		if r, ok := e.(*ResultEvent); ok {
			result = r
		}
	}
	if result == nil {
		t.Fatal("no ResultEvent")
	}
	if result.ContextSnapshot != nil {
		t.Errorf("ContextSnapshot should be nil, got %+v", result.ContextSnapshot)
	}
}

func TestContextSnapshotResetOnMessageStart(t *testing.T) {
	input := `{"type":"system","session_id":"s1","model":"opus"}
{"type":"stream_event","uuid":"u1","session_id":"s1","event":{"type":"message_start","message":{"model":"claude-opus-4-20250514","usage":{"input_tokens":50,"cache_read_input_tokens":1000,"cache_creation_input_tokens":100}}}}
{"type":"stream_event","uuid":"u2","session_id":"s1","event":{"type":"message_delta","usage":{"output_tokens":10}}}
{"type":"stream_event","uuid":"u3","session_id":"s1","event":{"type":"message_start","message":{"model":"claude-opus-4-20250514","usage":{"input_tokens":300,"cache_read_input_tokens":8000,"cache_creation_input_tokens":500}}}}
{"type":"stream_event","uuid":"u4","session_id":"s1","event":{"type":"message_delta","usage":{"output_tokens":77}}}
{"type":"result","subtype":"success","total_cost_usd":0.1,"usage":{"input_tokens":350,"output_tokens":87},"modelUsage":{"claude-opus-4-20250514":{"inputTokens":350,"outputTokens":87,"contextWindow":200000}}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var result *ResultEvent
	for e := range ch {
		if r, ok := e.(*ResultEvent); ok {
			result = r
		}
	}
	if result == nil {
		t.Fatal("no ResultEvent")
	}
	cs := result.ContextSnapshot
	if cs == nil {
		t.Fatal("ContextSnapshot is nil")
	}
	// Should have values from the LAST message_start/delta pair.
	if cs.InputTokens != 300 {
		t.Errorf("InputTokens = %d, want 300", cs.InputTokens)
	}
	if cs.CacheReadInputTokens != 8000 {
		t.Errorf("CacheReadInputTokens = %d, want 8000", cs.CacheReadInputTokens)
	}
	if cs.CacheCreationInputTokens != 500 {
		t.Errorf("CacheCreationInputTokens = %d, want 500", cs.CacheCreationInputTokens)
	}
	if cs.OutputTokens != 77 {
		t.Errorf("OutputTokens = %d, want 77", cs.OutputTokens)
	}
}

func TestContextSnapshotMessageStartOnly(t *testing.T) {
	input := `{"type":"system","session_id":"s1","model":"opus"}
{"type":"stream_event","uuid":"u1","session_id":"s1","event":{"type":"message_start","message":{"model":"claude-opus-4-20250514","usage":{"input_tokens":250,"cache_read_input_tokens":3000}}}}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":250,"output_tokens":0},"modelUsage":{"claude-opus-4-20250514":{"inputTokens":250,"contextWindow":200000}}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var result *ResultEvent
	for e := range ch {
		if r, ok := e.(*ResultEvent); ok {
			result = r
		}
	}
	if result == nil {
		t.Fatal("no ResultEvent")
	}
	cs := result.ContextSnapshot
	if cs == nil {
		t.Fatal("ContextSnapshot is nil")
	}
	if cs.InputTokens != 250 {
		t.Errorf("InputTokens = %d, want 250", cs.InputTokens)
	}
	if cs.CacheReadInputTokens != 3000 {
		t.Errorf("CacheReadInputTokens = %d, want 3000", cs.CacheReadInputTokens)
	}
	if cs.OutputTokens != 0 {
		t.Errorf("OutputTokens = %d, want 0", cs.OutputTokens)
	}
}

func TestContextSnapshotModelMismatch(t *testing.T) {
	input := `{"type":"system","session_id":"s1","model":"opus"}
{"type":"stream_event","uuid":"u1","session_id":"s1","event":{"type":"message_start","message":{"model":"claude-unknown","usage":{"input_tokens":100}}}}
{"type":"stream_event","uuid":"u2","session_id":"s1","event":{"type":"message_delta","usage":{"output_tokens":20}}}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":100,"output_tokens":20},"modelUsage":{"claude-opus-4-20250514":{"inputTokens":100,"outputTokens":20,"contextWindow":200000}}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var result *ResultEvent
	for e := range ch {
		if r, ok := e.(*ResultEvent); ok {
			result = r
		}
	}
	if result == nil {
		t.Fatal("no ResultEvent")
	}
	cs := result.ContextSnapshot
	if cs == nil {
		t.Fatal("ContextSnapshot is nil")
	}
	if cs.ContextWindow != 0 {
		t.Errorf("ContextWindow = %d, want 0 (model mismatch)", cs.ContextWindow)
	}
	if cs.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", cs.InputTokens)
	}
}

func TestContextSnapshotModelSuffixMatch(t *testing.T) {
	// The CLI appends a context-window suffix to modelUsage keys (e.g.,
	// "claude-opus-4-6[1m]") while inner stream events use the bare name.
	input := `{"type":"system","session_id":"s1","model":"claude-opus-4-6[1m]"}
{"type":"stream_event","uuid":"u1","session_id":"s1","event":{"type":"message_start","message":{"model":"claude-opus-4-6","usage":{"input_tokens":100}}}}
{"type":"stream_event","uuid":"u2","session_id":"s1","event":{"type":"message_delta","usage":{"output_tokens":50}}}
{"type":"result","subtype":"success","total_cost_usd":0.1,"usage":{"input_tokens":100,"output_tokens":50},"modelUsage":{"claude-opus-4-6[1m]":{"inputTokens":100,"outputTokens":50,"contextWindow":1000000,"maxOutputTokens":128000},"claude-haiku-4-5-20251001":{"inputTokens":8,"outputTokens":20,"contextWindow":200000,"maxOutputTokens":32000}}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var result *ResultEvent
	for e := range ch {
		if r, ok := e.(*ResultEvent); ok {
			result = r
		}
	}
	if result == nil {
		t.Fatal("no ResultEvent")
	}
	cs := result.ContextSnapshot
	if cs == nil {
		t.Fatal("ContextSnapshot is nil")
	}
	if cs.ContextWindow != 1000000 {
		t.Errorf("ContextWindow = %d, want 1000000", cs.ContextWindow)
	}
	if cs.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", cs.InputTokens)
	}
	if cs.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want 50", cs.OutputTokens)
	}
}

func TestParseUnknownEventType(t *testing.T) {
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"subagent_progress","agent_id":"abc123","data":"working"}
{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var events []Event
	for e := range ch {
		events = append(events, e)
	}

	var unknown *UnknownEvent
	for _, e := range events {
		if u, ok := e.(*UnknownEvent); ok {
			unknown = u
		}
	}
	if unknown == nil {
		t.Fatal("no UnknownEvent found")
	}
	if unknown.Type != "subagent_progress" {
		t.Errorf("Type = %q, want %q", unknown.Type, "subagent_progress")
	}
	// Verify Raw is valid JSON with original fields.
	var parsed map[string]any
	if err := json.Unmarshal(unknown.Raw, &parsed); err != nil {
		t.Fatalf("failed to unmarshal Raw: %v", err)
	}
	if parsed["agent_id"] != "abc123" {
		t.Errorf("agent_id = %v, want %q", parsed["agent_id"], "abc123")
	}
}

func TestParseUnknownContentBlock(t *testing.T) {
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"assistant","message":{"content":[{"type":"text","text":"hello"},{"type":"redacted_thinking","data":"secret"},{"type":"text","text":" world"}]}}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(), strings.NewReader(input), ch)
		close(ch)
	}()

	var events []Event
	for e := range ch {
		events = append(events, e)
	}

	var unknown *UnknownEvent
	var textCount int
	for _, e := range events {
		if u, ok := e.(*UnknownEvent); ok {
			unknown = u
		}
		if _, ok := e.(*TextEvent); ok {
			textCount++
		}
	}

	if textCount != 2 {
		t.Errorf("expected 2 TextEvents, got %d", textCount)
	}
	if unknown == nil {
		t.Fatal("no UnknownEvent for unrecognized content block")
	}
	if unknown.Type != "content/redacted_thinking" {
		t.Errorf("Type = %q, want %q", unknown.Type, "content/redacted_thinking")
	}
}

func TestParseUserEventToolResult(t *testing.T) {
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_abc","type":"tool_result","content":"file contents here"}]},"parent_tool_use_id":null,"session_id":"test","uuid":"uuid1","timestamp":"2026-03-29T18:36:37.512Z"}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var events []Event
	for e := range ch {
		events = append(events, e)
	}

	var ue *UserEvent
	for _, e := range events {
		if u, ok := e.(*UserEvent); ok {
			ue = u
		}
	}
	if ue == nil {
		t.Fatal("no UserEvent found")
	}
	if ue.ParentToolUseID != "" {
		t.Errorf("ParentToolUseID = %q, want empty (null)", ue.ParentToolUseID)
	}
	if ue.UUID != "uuid1" {
		t.Errorf("UUID = %q, want %q", ue.UUID, "uuid1")
	}
	if ue.Timestamp != "2026-03-29T18:36:37.512Z" {
		t.Errorf("Timestamp = %q", ue.Timestamp)
	}
	if len(ue.Content) != 1 {
		t.Fatalf("Content len = %d, want 1", len(ue.Content))
	}
	block := ue.Content[0]
	if block.Type != "tool_result" {
		t.Errorf("block.Type = %q, want %q", block.Type, "tool_result")
	}
	if block.ToolUseID != "toolu_abc" {
		t.Errorf("block.ToolUseID = %q, want %q", block.ToolUseID, "toolu_abc")
	}
	if len(block.Content) != 1 || block.Content[0].Text != "file contents here" {
		t.Errorf("block.Content = %v, want text 'file contents here'", block.Content)
	}
	if ue.AgentResult != nil {
		t.Error("AgentResult should be nil for non-agent tool results")
	}
}

func TestParseUserEventSubagentPrompt(t *testing.T) {
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Read go.mod"}]},"parent_tool_use_id":"toolu_agent1","session_id":"test","uuid":"uuid2","timestamp":"2026-03-29T18:36:53.939Z"}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var ue *UserEvent
	for e := range ch {
		if u, ok := e.(*UserEvent); ok {
			ue = u
		}
	}
	if ue == nil {
		t.Fatal("no UserEvent found")
	}
	if ue.ParentToolUseID != "toolu_agent1" {
		t.Errorf("ParentToolUseID = %q, want %q", ue.ParentToolUseID, "toolu_agent1")
	}
	if len(ue.Content) != 1 || ue.Content[0].Type != "text" {
		t.Fatalf("unexpected content: %v", ue.Content)
	}
	if ue.Text() != "Read go.mod" {
		t.Errorf("Text() = %q, want %q", ue.Text(), "Read go.mod")
	}
}

func TestParseUserEventAgentCompletion(t *testing.T) {
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_agent1","type":"tool_result","content":[{"type":"text","text":"module is foo/bar"}]}]},"parent_tool_use_id":null,"session_id":"test","uuid":"uuid3","timestamp":"2026-03-29T18:36:56.915Z","tool_use_result":{"status":"completed","prompt":"Read go.mod","agentId":"agent123","agentType":"Explore","content":[{"type":"text","text":"module is foo/bar"}],"totalDurationMs":2975,"totalTokens":21825,"totalToolUseCount":1}}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var ue *UserEvent
	for e := range ch {
		if u, ok := e.(*UserEvent); ok {
			ue = u
		}
	}
	if ue == nil {
		t.Fatal("no UserEvent found")
	}
	if ue.ParentToolUseID != "" {
		t.Errorf("ParentToolUseID = %q, want empty", ue.ParentToolUseID)
	}
	if ue.AgentResult == nil {
		t.Fatal("AgentResult is nil")
	}
	ar := ue.AgentResult
	if ar.Status != "completed" {
		t.Errorf("Status = %q, want %q", ar.Status, "completed")
	}
	if ar.AgentID != "agent123" {
		t.Errorf("AgentID = %q, want %q", ar.AgentID, "agent123")
	}
	if ar.AgentType != "Explore" {
		t.Errorf("AgentType = %q, want %q", ar.AgentType, "Explore")
	}
	if ar.Prompt != "Read go.mod" {
		t.Errorf("Prompt = %q, want %q", ar.Prompt, "Read go.mod")
	}
	if ar.TotalDurationMs != 2975 {
		t.Errorf("TotalDurationMs = %d, want 2975", ar.TotalDurationMs)
	}
	if ar.TotalTokens != 21825 {
		t.Errorf("TotalTokens = %d, want 21825", ar.TotalTokens)
	}
	if ar.TotalToolUseCount != 1 {
		t.Errorf("TotalToolUseCount = %d, want 1", ar.TotalToolUseCount)
	}
	if len(ar.Content) != 1 || ar.Content[0].Text != "module is foo/bar" {
		t.Errorf("Content = %v", ar.Content)
	}
	// The tool_result content block should also be parsed.
	if len(ue.Content) != 1 || ue.Content[0].ToolUseID != "toolu_agent1" {
		t.Errorf("Content blocks = %v", ue.Content)
	}
}

func TestParseTaskEvents(t *testing.T) {
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_agent1","name":"Agent","input":{"prompt":"read go.mod"}}]}}
{"type":"system","subtype":"task_started","task_id":"task1","tool_use_id":"toolu_agent1","description":"Read go.mod","task_type":"local_agent","prompt":"read go.mod","uuid":"u1","session_id":"test"}
{"type":"system","subtype":"task_progress","task_id":"task1","tool_use_id":"toolu_agent1","description":"Reading file","usage":{"total_tokens":5000,"tool_uses":1,"duration_ms":500},"last_tool_name":"Read","uuid":"u2","session_id":"test"}
{"type":"system","subtype":"task_notification","task_id":"task1","tool_use_id":"toolu_agent1","status":"completed","summary":"Read go.mod","usage":{"total_tokens":8000,"tool_uses":2,"duration_ms":1200},"uuid":"u3","session_id":"test"}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var tasks []*TaskEvent
	for e := range ch {
		if te, ok := e.(*TaskEvent); ok {
			tasks = append(tasks, te)
		}
	}

	if len(tasks) != 3 {
		t.Fatalf("got %d TaskEvents, want 3", len(tasks))
	}

	// task_started
	ts := tasks[0]
	if ts.Subtype != "task_started" {
		t.Errorf("tasks[0].Subtype = %q", ts.Subtype)
	}
	if ts.TaskID != "task1" || ts.ToolUseID != "toolu_agent1" {
		t.Errorf("tasks[0] IDs = %q, %q", ts.TaskID, ts.ToolUseID)
	}
	if ts.TaskType != "local_agent" {
		t.Errorf("tasks[0].TaskType = %q", ts.TaskType)
	}
	if ts.Description != "Read go.mod" {
		t.Errorf("tasks[0].Description = %q", ts.Description)
	}
	if ts.Prompt != "read go.mod" {
		t.Errorf("tasks[0].Prompt = %q", ts.Prompt)
	}

	// task_progress
	tp := tasks[1]
	if tp.Subtype != "task_progress" {
		t.Errorf("tasks[1].Subtype = %q", tp.Subtype)
	}
	if tp.LastToolName != "Read" {
		t.Errorf("tasks[1].LastToolName = %q", tp.LastToolName)
	}
	if tp.TotalTokens != 5000 || tp.ToolUses != 1 || tp.DurationMs != 500 {
		t.Errorf("tasks[1] usage = tokens:%d tools:%d ms:%d", tp.TotalTokens, tp.ToolUses, tp.DurationMs)
	}

	// task_notification
	tn := tasks[2]
	if tn.Subtype != "task_notification" {
		t.Errorf("tasks[2].Subtype = %q", tn.Subtype)
	}
	if tn.Status != "completed" {
		t.Errorf("tasks[2].Status = %q", tn.Status)
	}
	if tn.Summary != "Read go.mod" {
		t.Errorf("tasks[2].Summary = %q", tn.Summary)
	}
	if tn.TotalTokens != 8000 || tn.ToolUses != 2 || tn.DurationMs != 1200 {
		t.Errorf("tasks[2] usage = tokens:%d tools:%d ms:%d", tn.TotalTokens, tn.ToolUses, tn.DurationMs)
	}
}

func TestParseHookEvents(t *testing.T) {
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"system","subtype":"hook_started","hook_id":"h1","hook_name":"SessionStart:startup","hook_event":"SessionStart","uuid":"u1","session_id":"test"}
{"type":"system","subtype":"hook_response","hook_id":"h1","hook_name":"SessionStart:startup","hook_event":"SessionStart","output":"loaded","stdout":"loaded","stderr":"","exit_code":0,"outcome":"success","uuid":"u2","session_id":"test"}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(), strings.NewReader(input), ch)
		close(ch)
	}()

	var hooks []*HookEvent
	for e := range ch {
		if h, ok := e.(*HookEvent); ok {
			hooks = append(hooks, h)
		}
	}

	if len(hooks) != 2 {
		t.Fatalf("got %d HookEvents, want 2", len(hooks))
	}

	started := hooks[0]
	if started.Subtype != "hook_started" {
		t.Errorf("hooks[0].Subtype = %q, want hook_started", started.Subtype)
	}
	if started.HookID != "h1" || started.HookName != "SessionStart:startup" || started.HookEvent != "SessionStart" {
		t.Errorf("hooks[0] ids = %q, %q, %q", started.HookID, started.HookName, started.HookEvent)
	}
	if started.ExitCode != 0 || started.Outcome != "" || started.Output != "" {
		t.Errorf("hooks[0] should have empty response fields, got exit=%d outcome=%q output=%q", started.ExitCode, started.Outcome, started.Output)
	}

	resp := hooks[1]
	if resp.Subtype != "hook_response" {
		t.Errorf("hooks[1].Subtype = %q, want hook_response", resp.Subtype)
	}
	if resp.Output != "loaded" || resp.Stdout != "loaded" {
		t.Errorf("hooks[1] output = %q, stdout = %q", resp.Output, resp.Stdout)
	}
	if resp.Outcome != "success" || resp.ExitCode != 0 {
		t.Errorf("hooks[1] outcome = %q, exit = %d", resp.Outcome, resp.ExitCode)
	}
	if len(resp.Raw) == 0 {
		t.Error("hooks[1].Raw should be populated")
	}
}

func TestParseHookEventNonZeroExit(t *testing.T) {
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"system","subtype":"hook_response","hook_id":"h2","hook_name":"PreToolUse","hook_event":"PreToolUse","output":"","stdout":"","stderr":"boom","exit_code":2,"outcome":"failure","uuid":"u","session_id":"test"}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(), strings.NewReader(input), ch)
		close(ch)
	}()

	var h *HookEvent
	for e := range ch {
		if he, ok := e.(*HookEvent); ok {
			h = he
		}
	}
	if h == nil {
		t.Fatal("no HookEvent")
	}
	if h.ExitCode != 2 || h.Outcome != "failure" || h.Stderr != "boom" {
		t.Errorf("exit=%d outcome=%q stderr=%q", h.ExitCode, h.Outcome, h.Stderr)
	}
}

func TestParseParentToolUseID(t *testing.T) {
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_sub_read","name":"Read","input":{"path":"go.mod"}}]},"parent_tool_use_id":"toolu_agent1","session_id":"test"}
{"type":"assistant","message":{"content":[{"type":"text","text":"top-level text"}]},"parent_tool_use_id":null,"session_id":"test"}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var events []Event
	for e := range ch {
		events = append(events, e)
	}

	// Find the subagent ToolUseEvent — should have ParentToolUseID set.
	var toolUse *ToolUseEvent
	for _, e := range events {
		if tu, ok := e.(*ToolUseEvent); ok {
			toolUse = tu
		}
	}
	if toolUse == nil {
		t.Fatal("no ToolUseEvent")
	}
	if toolUse.ParentToolUseID != "toolu_agent1" {
		t.Errorf("ToolUseEvent.ParentToolUseID = %q, want %q", toolUse.ParentToolUseID, "toolu_agent1")
	}

	// Find the top-level TextEvent — ParentToolUseID should be empty.
	var text *TextEvent
	for _, e := range events {
		if te, ok := e.(*TextEvent); ok {
			text = te
		}
	}
	if text == nil {
		t.Fatal("no TextEvent")
	}
	if text.ParentToolUseID != "" {
		t.Errorf("TextEvent.ParentToolUseID = %q, want empty", text.ParentToolUseID)
	}
}

// extractContent fallback: non-string, non-array content wraps as text.
func TestParseToolResultFallbackContent(t *testing.T) {
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"assistant","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":42}]}}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var tr *ToolResultEvent
	for e := range ch {
		if r, ok := e.(*ToolResultEvent); ok {
			tr = r
		}
	}
	if tr == nil {
		t.Fatal("no ToolResultEvent")
	}
	if len(tr.Content) != 1 || tr.Content[0].Type != "text" {
		t.Errorf("expected fallback text content, got %v", tr.Content)
	}
	if tr.Content[0].Text != "42" {
		t.Errorf("fallback text = %q, want '42'", tr.Content[0].Text)
	}
}

// stream_event with message_delta fills output_tokens in context snapshot.
func TestParseStreamEventMessageDelta(t *testing.T) {
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"stream_event","uuid":"u1","session_id":"test","event":{"type":"message_start","message":{"model":"claude-sonnet","usage":{"input_tokens":100,"cache_read_input_tokens":50}}}}
{"type":"stream_event","uuid":"u2","session_id":"test","event":{"type":"message_delta","usage":{"output_tokens":200}}}
{"type":"result","subtype":"success","session_id":"test","total_cost_usd":0.05,"usage":{"input_tokens":100,"output_tokens":200}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var result *ResultEvent
	for e := range ch {
		if r, ok := e.(*ResultEvent); ok {
			result = r
		}
	}
	if result == nil {
		t.Fatal("no ResultEvent")
	}
	if result.ContextSnapshot == nil {
		t.Fatal("ContextSnapshot is nil — message_start should have created it")
	}
	if result.ContextSnapshot.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", result.ContextSnapshot.InputTokens)
	}
	if result.ContextSnapshot.CacheReadInputTokens != 50 {
		t.Errorf("CacheReadInputTokens = %d, want 50", result.ContextSnapshot.CacheReadInputTokens)
	}
	if result.ContextSnapshot.OutputTokens != 200 {
		t.Errorf("OutputTokens = %d, want 200", result.ContextSnapshot.OutputTokens)
	}
}

// message_delta without prior message_start should not panic.
func TestParseStreamEventDeltaWithoutStart(t *testing.T) {
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"stream_event","uuid":"u1","session_id":"test","event":{"type":"message_delta","usage":{"output_tokens":100}}}
{"type":"result","subtype":"success","session_id":"test","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var result *ResultEvent
	for e := range ch {
		if r, ok := e.(*ResultEvent); ok {
			result = r
		}
	}
	if result == nil {
		t.Fatal("no ResultEvent")
	}
	// No snapshot because message_start never arrived
	if result.ContextSnapshot != nil {
		t.Errorf("expected nil ContextSnapshot without message_start, got %+v", result.ContextSnapshot)
	}
}

func TestParseUserEventReplay(t *testing.T) {
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"user","message":{"role":"user","content":"What is 2+2?"},"session_id":"test","parent_tool_use_id":null,"uuid":"replay-uuid","timestamp":"2026-03-30T12:12:51.336Z","isReplay":true}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var ue *UserEvent
	for e := range ch {
		if u, ok := e.(*UserEvent); ok {
			ue = u
		}
	}
	if ue == nil {
		t.Fatal("no UserEvent found")
	}
	if !ue.IsReplay {
		t.Error("IsReplay = false, want true")
	}
	if ue.UUID != "replay-uuid" {
		t.Errorf("UUID = %q, want replay-uuid", ue.UUID)
	}
	if ue.Text() != "What is 2+2?" {
		t.Errorf("Text() = %q, want %q", ue.Text(), "What is 2+2?")
	}
	if len(ue.Content) != 1 || ue.Content[0].Type != "text" {
		t.Errorf("Content = %+v, want 1 text block", ue.Content)
	}
}

func TestParseUserEventReplayFalseByDefault(t *testing.T) {
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]},"session_id":"test","uuid":"u1"}
{"type":"result","subtype":"success","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(),strings.NewReader(input), ch)
		close(ch)
	}()

	var ue *UserEvent
	for e := range ch {
		if u, ok := e.(*UserEvent); ok {
			ue = u
		}
	}
	if ue == nil {
		t.Fatal("no UserEvent found")
	}
	if ue.IsReplay {
		t.Error("IsReplay should be false for normal user events")
	}
}

func TestParseMaxTurnsStream(t *testing.T) {
	events := collectEvents(t, "testdata/max_turns.jsonl")

	// Should have ErrorEvent with ErrMaxTurns before ResultEvent.
	var maxTurnsErr *ErrorEvent
	var result *ResultEvent
	for _, e := range events {
		switch ev := e.(type) {
		case *ErrorEvent:
			if errors.Is(ev.Err, ErrMaxTurns) {
				maxTurnsErr = ev
			}
		case *ResultEvent:
			result = ev
		}
	}

	if maxTurnsErr == nil {
		t.Fatal("no ErrorEvent with ErrMaxTurns found")
	}
	if maxTurnsErr.Fatal {
		t.Error("max turns ErrorEvent should be non-fatal")
	}
	var mte *MaxTurnsError
	if !errors.As(maxTurnsErr.Err, &mte) {
		t.Fatal("expected errors.As to match *MaxTurnsError")
	}
	if mte.Turns != 3 {
		t.Errorf("MaxTurnsError.Turns = %d, want 3", mte.Turns)
	}
	if !strings.Contains(mte.Message, "Reached maximum number of turns (3)") {
		t.Errorf("MaxTurnsError.Message = %q", mte.Message)
	}

	if result == nil {
		t.Fatal("no ResultEvent found")
	}
	if result.Subtype != "error_max_turns" {
		t.Errorf("ResultEvent.Subtype = %q, want error_max_turns", result.Subtype)
	}
	if result.NumTurns != 3 {
		t.Errorf("ResultEvent.NumTurns = %d, want 3", result.NumTurns)
	}
}

func TestParseResultNumTurns(t *testing.T) {
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}
{"type":"result","subtype":"success","num_turns":5,"total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(), strings.NewReader(input), ch)
		close(ch)
	}()

	var result *ResultEvent
	for e := range ch {
		if r, ok := e.(*ResultEvent); ok {
			result = r
		}
	}
	if result == nil {
		t.Fatal("no ResultEvent")
	}
	if result.NumTurns != 5 {
		t.Errorf("NumTurns = %d, want 5", result.NumTurns)
	}
}

func TestParseTurnEvents(t *testing.T) {
	events := collectEvents(t, "testdata/max_turns.jsonl")

	var turns []*TurnEvent
	for _, e := range events {
		if te, ok := e.(*TurnEvent); ok {
			turns = append(turns, te)
		}
	}

	if len(turns) != 3 {
		t.Fatalf("got %d TurnEvents, want 3", len(turns))
	}
	// Turn 1: tool_use Read
	if turns[0].Turn != 1 || turns[0].ToolName != "Read" {
		t.Errorf("turn 1: Turn=%d, ToolName=%q", turns[0].Turn, turns[0].ToolName)
	}
	// Turn 2: tool_use Grep
	if turns[1].Turn != 2 || turns[1].ToolName != "Grep" {
		t.Errorf("turn 2: Turn=%d, ToolName=%q", turns[1].Turn, turns[1].ToolName)
	}
	// Turn 3: text only (no tool)
	if turns[2].Turn != 3 || turns[2].ToolName != "" {
		t.Errorf("turn 3: Turn=%d, ToolName=%q", turns[2].Turn, turns[2].ToolName)
	}
}

func TestParseTurnEventsSkipSubagent(t *testing.T) {
	// Subagent assistant messages (with parent_tool_use_id) should NOT emit TurnEvents.
	input := `{"type":"system","session_id":"test","model":"sonnet"}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu_agent","name":"Agent","input":{}}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu_sub","name":"Read","input":{}}]},"parent_tool_use_id":"tu_agent"}
{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}
{"type":"result","subtype":"success","num_turns":2,"total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}
`
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(), strings.NewReader(input), ch)
		close(ch)
	}()

	var turns []*TurnEvent
	for e := range ch {
		if te, ok := e.(*TurnEvent); ok {
			turns = append(turns, te)
		}
	}

	// Only 2 top-level assistant messages (turn 1: Agent tool, turn 2: text)
	if len(turns) != 2 {
		t.Fatalf("got %d TurnEvents, want 2", len(turns))
	}
	if turns[0].ToolName != "Agent" {
		t.Errorf("turn 1 ToolName = %q, want Agent", turns[0].ToolName)
	}
	if turns[1].ToolName != "" {
		t.Errorf("turn 2 ToolName = %q, want empty", turns[1].ToolName)
	}
}
