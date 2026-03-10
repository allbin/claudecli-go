package claudecli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// ParseEvents reads JSONL from r and sends parsed events to ch.
// Does not close ch — the caller is responsible for closing it.
// Safe to call from a goroutine.
func ParseEvents(r io.Reader, ch chan<- Event) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 256*1024), 10*1024*1024)

	var resultText []string

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var raw rawEvent
		if err := json.Unmarshal(line, &raw); err != nil {
			ch <- &ErrorEvent{Err: fmt.Errorf("unmarshal JSONL: %w", err)}
			continue
		}

		switch raw.Type {
		case "system":
			ch <- &InitEvent{
				SessionID: raw.SessionID,
				Model:     raw.Model,
				Tools:     raw.Tools,
			}

		case "assistant":
			if raw.Message == nil {
				continue
			}
			for _, block := range raw.Message.Content {
				parseContentBlock(block, &resultText, ch)
			}

		case "result":
			ch <- &ResultEvent{
				Text:             strings.Join(resultText, ""),
				Subtype:          raw.Subtype,
				StopReason:       raw.StopReason,
				StructuredOutput: raw.StructuredOutput,
				Duration:         time.Duration(raw.DurationMS) * time.Millisecond,
				CostUSD:          raw.CostUSD,
				SessionID:        raw.SessionID,
				Usage: Usage{
					InputTokens:       raw.Usage.InputTokens,
					OutputTokens:      raw.Usage.OutputTokens,
					CacheReadTokens:   raw.Usage.CacheReadInputTokens,
					CacheCreateTokens: raw.Usage.CacheCreationInputTokens,
				},
			}
			// Result is the terminal event. Return immediately to avoid
			// blocking on scanner.Scan() if the CLI keeps stdout open (known bug).
			return

		case "rate_limit_event":
			ch <- &RateLimitEvent{
				Status:      raw.RateLimitStatus,
				Utilization: raw.RateLimitUtilization,
			}

		case "control_request":
			var body rawControlRequestBody
			if err := json.Unmarshal(raw.Request, &body); err != nil {
				ch <- &ErrorEvent{Err: fmt.Errorf("unmarshal control request: %w", err)}
				continue
			}
			ch <- &ControlRequestEvent{
				RequestID: raw.RequestID,
				Subtype:   body.Subtype,
				Body:      raw.Request,
			}

		case "stream_event":
			ch <- &StreamEvent{
				UUID:      raw.UUID,
				SessionID: raw.SessionID,
				Event:     raw.Event,
			}
		}
	}

	if err := scanner.Err(); err != nil {
		ch <- &ErrorEvent{Err: fmt.Errorf("scanner: %w", err)}
	}
}

func parseContentBlock(block rawContent, resultText *[]string, ch chan<- Event) {
	switch block.Type {
	case "thinking":
		ch <- &ThinkingEvent{Content: block.Thinking, Signature: block.Signature}
	case "text":
		*resultText = append(*resultText, block.Text)
		ch <- &TextEvent{Content: block.Text}
	case "tool_use":
		ch <- &ToolUseEvent{
			ID:    block.ID,
			Name:  block.Name,
			Input: block.Input,
		}
	case "tool_result":
		ch <- &ToolResultEvent{
			ToolUseID: block.ToolUseID,
			Content:   extractContent(block.Content),
		}
	}
}

// extractContent handles both string and array forms of tool result content.
// String form: "some text"
// Array form:  [{"type":"text","text":"some text"}, ...]
func extractContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	// Try string first.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	// Try array of content blocks.
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "")
	}

	// Fallback: return raw JSON as-is.
	return string(raw)
}

// rawEvent is the internal representation of a JSONL line from the CLI.
type rawEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`

	// system event
	SessionID string   `json:"session_id,omitempty"`
	Model     string   `json:"model,omitempty"`
	Tools     []string `json:"tools,omitempty"`

	// assistant event
	Message *rawMessage `json:"message,omitempty"`

	// result event
	Result           string          `json:"result,omitempty"`
	DurationMS       float64         `json:"duration_ms,omitempty"`
	CostUSD          float64         `json:"total_cost_usd,omitempty"`
	StopReason       string          `json:"stop_reason,omitempty"`
	StructuredOutput json.RawMessage `json:"structured_output,omitempty"`
	Usage            rawUsage        `json:"usage,omitempty"`

	// rate_limit_event
	RateLimitStatus      string  `json:"status,omitempty"`
	RateLimitUtilization float64 `json:"utilization,omitempty"`

	// control_request
	RequestID string          `json:"request_id,omitempty"`
	Request   json.RawMessage `json:"request,omitempty"`

	// stream_event
	UUID  string          `json:"uuid,omitempty"`
	Event json.RawMessage `json:"event,omitempty"`
}

type rawMessage struct {
	Content []rawContent `json:"content"`
}

type rawContent struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
}

type rawUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}
