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
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

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
			switch raw.Subtype {
			case "init", "":
				ch <- &InitEvent{
					SessionID: raw.SessionID,
					Model:     raw.Model,
					Tools:     raw.Tools,
				}
			case "status":
				status := ""
				if raw.Status != nil {
					status = *raw.Status
				}
				ch <- &CompactStatusEvent{
					SessionID: raw.SessionID,
					Status:    status,
				}
			case "compact_boundary":
				ch <- parseCompactBoundaryEvent(&raw)
			}

		case "assistant":
			if raw.Message == nil {
				continue
			}
			for _, block := range raw.Message.Content {
				parseContentBlock(block, &resultText, ch)
			}
			if len(raw.Message.ContextManagement) > 0 && string(raw.Message.ContextManagement) != "null" {
				ch <- &ContextManagementEvent{Raw: raw.Message.ContextManagement}
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
				Usage:            raw.Usage.toUsage(),
				ModelUsage:       convertModelUsage(raw.ModelUsage),
			}
			// Result is the terminal event. Return immediately to avoid
			// blocking on scanner.Scan() if the CLI keeps stdout open (known bug).
			return

		case "rate_limit_event":
			ch <- parseRateLimitEvent(&raw)

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
// Array form:  [{"type":"text","text":"..."}, {"type":"image","source":{...}}, ...]
func extractContent(raw json.RawMessage) []ToolContent {
	if len(raw) == 0 {
		return nil
	}

	// Try string first.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []ToolContent{{Type: "text", Text: s}}
	}

	// Try array of content blocks.
	var blocks []struct {
		Type   string `json:"type"`
		Text   string `json:"text,omitempty"`
		Source *struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source,omitempty"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var result []ToolContent
		for _, b := range blocks {
			switch b.Type {
			case "text":
				result = append(result, ToolContent{Type: "text", Text: b.Text})
			case "image":
				if b.Source != nil {
					result = append(result, ToolContent{
						Type:      "image",
						MediaType: b.Source.MediaType,
						Data:      b.Source.Data,
					})
				}
			}
		}
		return result
	}

	// Fallback: wrap raw JSON as text.
	return []ToolContent{{Type: "text", Text: string(raw)}}
}

func parseRateLimitEvent(raw *rawEvent) *RateLimitEvent {
	// Build raw map for forward compat. Use the pre-parsed struct fields
	// plus the original JSON map if available.
	rawMap := raw.RateLimitInfo.Raw
	if rawMap == nil {
		rawMap = make(map[string]any)
	}
	return &RateLimitEvent{
		Status:                raw.RateLimitInfo.Status,
		Utilization:           raw.RateLimitInfo.Utilization,
		ResetsAt:              raw.RateLimitInfo.ResetsAt,
		RateLimitType:         raw.RateLimitInfo.RateLimitType,
		OverageStatus:         raw.RateLimitInfo.OverageStatus,
		OverageResetsAt:       raw.RateLimitInfo.OverageResetsAt,
		OverageDisabledReason: raw.RateLimitInfo.OverageDisabledReason,
		UUID:                  raw.UUID,
		SessionID:             raw.SessionID,
		Raw:                   rawMap,
	}
}

func parseCompactBoundaryEvent(raw *rawEvent) *CompactBoundaryEvent {
	ev := &CompactBoundaryEvent{
		SessionID: raw.SessionID,
		Raw:       raw.CompactMetadata,
	}
	if len(raw.CompactMetadata) > 0 {
		var meta struct {
			Trigger   string `json:"trigger"`
			PreTokens int    `json:"pre_tokens"`
		}
		if err := json.Unmarshal(raw.CompactMetadata, &meta); err == nil {
			ev.Trigger = meta.Trigger
			ev.PreTokens = meta.PreTokens
		}
	}
	return ev
}

// rawEvent is the internal representation of a JSONL line from the CLI.
type rawEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`

	// system event (init subtype)
	SessionID string   `json:"session_id,omitempty"`
	Model     string   `json:"model,omitempty"`
	Tools     []string `json:"tools,omitempty"`

	// system event (status subtype)
	Status *string `json:"status"`

	// system event (compact_boundary subtype)
	CompactMetadata json.RawMessage `json:"compact_metadata,omitempty"`

	// assistant event
	Message *rawMessage `json:"message,omitempty"`

	// result event
	Result           string          `json:"result,omitempty"`
	DurationMS       float64         `json:"duration_ms,omitempty"`
	CostUSD          float64         `json:"total_cost_usd,omitempty"`
	StopReason       string          `json:"stop_reason,omitempty"`
	StructuredOutput json.RawMessage `json:"structured_output,omitempty"`
	Usage            rawUsage                   `json:"usage,omitempty"`
	ModelUsage       map[string]rawModelUsage   `json:"modelUsage,omitempty"`

	// rate_limit_event
	RateLimitInfo rawRateLimitInfo `json:"rate_limit_info,omitempty"`

	// control_request
	RequestID string          `json:"request_id,omitempty"`
	Request   json.RawMessage `json:"request,omitempty"`

	// stream_event
	UUID  string          `json:"uuid,omitempty"`
	Event json.RawMessage `json:"event,omitempty"`
}

type rawMessage struct {
	Content           []rawContent    `json:"content"`
	ContextManagement json.RawMessage `json:"context_management,omitempty"`
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

type rawRateLimitInfo struct {
	Status                string         `json:"status"`
	Utilization           float64        `json:"utilization"`
	ResetsAt              int64          `json:"resetsAt"`
	RateLimitType         string         `json:"rateLimitType"`
	OverageStatus         string         `json:"overageStatus"`
	OverageResetsAt       int64          `json:"overageResetsAt"`
	OverageDisabledReason string         `json:"overageDisabledReason"`
	Raw                   map[string]any `json:"-"`
}

func (r *rawRateLimitInfo) UnmarshalJSON(data []byte) error {
	// Unmarshal known fields via alias to avoid recursion.
	type alias rawRateLimitInfo
	if err := json.Unmarshal(data, (*alias)(r)); err != nil {
		return err
	}
	// Preserve full raw map for forward compat.
	_ = json.Unmarshal(data, &r.Raw)
	return nil
}

type rawUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

func (r rawUsage) toUsage() Usage {
	return Usage{
		InputTokens:       r.InputTokens,
		OutputTokens:      r.OutputTokens,
		CacheReadTokens:   r.CacheReadInputTokens,
		CacheCreateTokens: r.CacheCreationInputTokens,
	}
}

type rawModelUsage struct {
	InputTokens              int     `json:"inputTokens"`
	OutputTokens             int     `json:"outputTokens"`
	CacheReadInputTokens     int     `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int     `json:"cacheCreationInputTokens"`
	CostUSD                  float64 `json:"costUSD"`
	ContextWindow            int     `json:"contextWindow"`
	MaxOutputTokens          int     `json:"maxOutputTokens"`
	WebSearchRequests        int     `json:"webSearchRequests"`
	WebFetchRequests         int     `json:"webFetchRequests"`
}

func convertModelUsage(raw map[string]rawModelUsage) map[string]ModelUsage {
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]ModelUsage, len(raw))
	for k, v := range raw {
		out[k] = ModelUsage{
			InputTokens:       v.InputTokens,
			OutputTokens:      v.OutputTokens,
			CacheReadTokens:   v.CacheReadInputTokens,
			CacheCreateTokens: v.CacheCreationInputTokens,
			CostUSD:           v.CostUSD,
			ContextWindow:     v.ContextWindow,
			MaxOutputTokens:   v.MaxOutputTokens,
			WebSearchRequests: v.WebSearchRequests,
			WebFetchRequests:  v.WebFetchRequests,
		}
	}
	return out
}
