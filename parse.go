package claudecli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// ParseEvents reads JSONL from r and sends parsed events to ch.
// Does not close ch — the caller is responsible for closing it.
// Safe to call from a goroutine.
//
// When ctx is cancelled, ParseEvents stops processing new lines and returns.
// Note: cancellation does not unblock a pending scanner.Scan() — the caller
// must close the reader (e.g. by killing the subprocess) to unblock reads.
func ParseEvents(ctx context.Context, r io.Reader, ch chan<- Event) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	var resultText []string
	var snapshot *ContextSnapshot
	var lastModel string

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
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
					SessionID:  raw.SessionID,
					Model:      raw.Model,
					Tools:      raw.Tools,
					Agents:     raw.Agents,
					Skills:     raw.Skills,
					MCPServers: raw.MCPServers,
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
			case "task_started", "task_progress", "task_notification":
				ch <- parseTaskEvent(&raw, line)
			default:
				ch <- &UnknownEvent{
					Type: "system/" + raw.Subtype,
					Raw:  append(json.RawMessage(nil), line...),
				}
			}

		case "assistant":
			if raw.Message == nil {
				continue
			}
			parentToolUseID := ""
			if raw.ParentToolUseID != nil {
				parentToolUseID = *raw.ParentToolUseID
			}
			for _, block := range raw.Message.Content {
				parseContentBlock(block, parentToolUseID, &resultText, ch)
			}
			if len(raw.Message.ContextManagement) > 0 && string(raw.Message.ContextManagement) != "null" {
				ch <- &ContextManagementEvent{Raw: raw.Message.ContextManagement}
			}

		case "result":
			modelUsage := convertModelUsage(raw.ModelUsage)
			if snapshot != nil && lastModel != "" {
				if mu, ok := lookupModelUsage(modelUsage, lastModel); ok {
					snapshot.ContextWindow = mu.ContextWindow
				}
			}
			ch <- &ResultEvent{
				Text:             strings.Join(resultText, ""),
				Subtype:          raw.Subtype,
				StopReason:       raw.StopReason,
				StructuredOutput: raw.StructuredOutput,
				Duration:         time.Duration(raw.DurationMS) * time.Millisecond,
				CostUSD:          raw.CostUSD,
				SessionID:        raw.SessionID,
				Usage:            raw.Usage.toUsage(),
				ModelUsage:       modelUsage,
				ContextSnapshot:  snapshot,
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
			updateContextSnapshot(raw.Event, &snapshot, &lastModel)

		case "error":
			ch <- parseErrorEvent(&raw)

		case "user":
			ch <- parseUserEvent(&raw)

		default:
			ch <- &UnknownEvent{
				Type: raw.Type,
				Raw:  append(json.RawMessage(nil), line...),
			}
		}
	}

	if err := scanner.Err(); err != nil {
		ch <- &ErrorEvent{Err: fmt.Errorf("scanner: %w", err)}
	}
}

func parseContentBlock(block rawContent, parentToolUseID string, resultText *[]string, ch chan<- Event) {
	switch block.Type {
	case "thinking":
		ch <- &ThinkingEvent{Content: block.Thinking, Signature: block.Signature, ParentToolUseID: parentToolUseID}
	case "text":
		*resultText = append(*resultText, block.Text)
		ch <- &TextEvent{Content: block.Text, ParentToolUseID: parentToolUseID}
	case "tool_use":
		ch <- &ToolUseEvent{
			ID:              block.ID,
			Name:            block.Name,
			Input:           block.Input,
			ParentToolUseID: parentToolUseID,
		}
	case "tool_result":
		ch <- &ToolResultEvent{
			ToolUseID:       block.ToolUseID,
			Content:         extractContent(block.Content),
			ParentToolUseID: parentToolUseID,
		}
	default:
		if block.Type != "" {
			raw, _ := json.Marshal(block)
			ch <- &UnknownEvent{
				Type: "content/" + block.Type,
				Raw:  raw,
			}
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

func parseErrorEvent(raw *rawEvent) *ErrorEvent {
	var errObj struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if len(raw.ErrorData) > 0 {
		_ = json.Unmarshal(raw.ErrorData, &errObj)
	}

	msg := errObj.Message
	if msg == "" {
		msg = "unknown error"
	}

	d := &errorDetails{
		typ:     normalizeAPIErrorType(errObj.Type),
		message: msg,
	}
	classified := classifyError(d)
	if classified == nil {
		classified = fmt.Errorf("%w: %s: %s", ErrAPI, errObj.Type, msg)
	}

	return &ErrorEvent{Err: classified, Fatal: false}
}

// rawEvent is the internal representation of a JSONL line from the CLI.
type rawEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`

	// system event (init subtype)
	SessionID  string            `json:"session_id,omitempty"`
	Model      string            `json:"model,omitempty"`
	Tools      []string          `json:"tools,omitempty"`
	Agents     []string          `json:"agents,omitempty"`
	Skills     []string          `json:"skills,omitempty"`
	MCPServers []MCPServerStatus `json:"mcp_servers,omitempty"`

	// system event (status subtype)
	Status *string `json:"status"`

	// system event (compact_boundary subtype)
	CompactMetadata json.RawMessage `json:"compact_metadata,omitempty"`

	// system task subtypes (task_started, task_progress, task_notification)
	TaskID       string `json:"task_id,omitempty"`
	ToolUseID    string `json:"tool_use_id,omitempty"`
	Description  string `json:"description,omitempty"`
	TaskType     string `json:"task_type,omitempty"`
	Prompt       string `json:"prompt,omitempty"`
	LastToolName string `json:"last_tool_name,omitempty"`
	Summary      string `json:"summary,omitempty"`

	// assistant + user events
	Message         *rawMessage     `json:"message,omitempty"`
	ParentToolUseID *string         `json:"parent_tool_use_id,omitempty"`
	Timestamp       string          `json:"timestamp,omitempty"`
	ToolUseResult   json.RawMessage `json:"tool_use_result,omitempty"`
	IsReplay        bool            `json:"isReplay,omitempty"`

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

	// error event
	ErrorData json.RawMessage `json:"error,omitempty"`
}

type rawMessage struct {
	Content           rawFlexContent  `json:"content"`
	ContextManagement json.RawMessage `json:"context_management,omitempty"`
}

// rawFlexContent handles the CLI's content field which can be either a plain
// string (replay user messages) or an array of content blocks (assistant/tool).
type rawFlexContent []rawContent

func (c *rawFlexContent) UnmarshalJSON(data []byte) error {
	// Try array first (common case).
	var blocks []rawContent
	if err := json.Unmarshal(data, &blocks); err == nil {
		*c = blocks
		return nil
	}
	// Fall back to plain string (replay user messages).
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*c = []rawContent{{Type: "text", Text: s}}
		return nil
	}
	// Ignore unparseable content.
	*c = nil
	return nil
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
	// Task event fields (task_progress, task_notification).
	TotalTokens int `json:"total_tokens"`
	ToolUses    int `json:"tool_uses"`
	DurationMs  int `json:"duration_ms"`
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

// lookupModelUsage finds the ModelUsage entry for the given model name.
// The CLI may append a context-window suffix (e.g., "claude-opus-4-6[1m]")
// to modelUsage keys while inner stream events use the bare model name.
func lookupModelUsage(mu map[string]ModelUsage, model string) (ModelUsage, bool) {
	if v, ok := mu[model]; ok {
		return v, true
	}
	prefix := model + "["
	for k, v := range mu {
		if strings.HasPrefix(k, prefix) {
			return v, true
		}
	}
	return ModelUsage{}, false
}

// rawInnerEventType peeks at just the "type" field of an inner stream event.
type rawInnerEventType struct {
	Type string `json:"type"`
}

type rawMessageStart struct {
	Message struct {
		Model string       `json:"model"`
		Usage rawInnerUsage `json:"usage"`
	} `json:"message"`
}

type rawMessageDelta struct {
	Usage rawInnerUsage `json:"usage"`
}

type rawInnerUsage struct {
	InputTokens              int `json:"input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	OutputTokens             int `json:"output_tokens"`
}

// updateContextSnapshot inspects a raw inner stream event for message_start or
// message_delta usage data. On message_start it resets the snapshot and records
// the model. On message_delta it fills in output_tokens.
func updateContextSnapshot(innerEvent json.RawMessage, snapshot **ContextSnapshot, lastModel *string) {
	if len(innerEvent) == 0 {
		return
	}
	var peek rawInnerEventType
	if err := json.Unmarshal(innerEvent, &peek); err != nil {
		return
	}
	switch peek.Type {
	case "message_start":
		var ms rawMessageStart
		if err := json.Unmarshal(innerEvent, &ms); err != nil {
			return
		}
		*snapshot = &ContextSnapshot{
			InputTokens:              ms.Message.Usage.InputTokens,
			CacheReadInputTokens:     ms.Message.Usage.CacheReadInputTokens,
			CacheCreationInputTokens: ms.Message.Usage.CacheCreationInputTokens,
		}
		*lastModel = ms.Message.Model
	case "message_delta":
		if *snapshot == nil {
			return
		}
		var md rawMessageDelta
		if err := json.Unmarshal(innerEvent, &md); err != nil {
			return
		}
		(*snapshot).OutputTokens = md.Usage.OutputTokens
	}
}

func parseUserEvent(raw *rawEvent) *UserEvent {
	ev := &UserEvent{
		SessionID: raw.SessionID,
		UUID:      raw.UUID,
		Timestamp: raw.Timestamp,
		IsReplay:  raw.IsReplay,
	}
	if raw.ParentToolUseID != nil {
		ev.ParentToolUseID = *raw.ParentToolUseID
	}

	if raw.Message != nil {
		for _, block := range raw.Message.Content {
			uc := UserContent{Type: block.Type}
			switch block.Type {
			case "text":
				uc.Text = block.Text
			case "tool_result":
				uc.ToolUseID = block.ToolUseID
				uc.Content = extractContent(block.Content)
			}
			ev.Content = append(ev.Content, uc)
		}
	}

	if len(raw.ToolUseResult) > 0 {
		ev.AgentResult = parseAgentResult(raw.ToolUseResult)
	}

	return ev
}

type rawAgentResult struct {
	Status            string       `json:"status"`
	Prompt            string       `json:"prompt"`
	AgentID           string       `json:"agentId"`
	AgentType         string       `json:"agentType"`
	Content           []rawContent `json:"content"`
	TotalDurationMs   int          `json:"totalDurationMs"`
	TotalTokens       int          `json:"totalTokens"`
	TotalToolUseCount int          `json:"totalToolUseCount"`
}

func parseAgentResult(data json.RawMessage) *AgentResult {
	var raw rawAgentResult
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	ar := &AgentResult{
		Status:            raw.Status,
		Prompt:            raw.Prompt,
		AgentID:           raw.AgentID,
		AgentType:         raw.AgentType,
		TotalDurationMs:   raw.TotalDurationMs,
		TotalTokens:       raw.TotalTokens,
		TotalToolUseCount: raw.TotalToolUseCount,
	}
	for _, block := range raw.Content {
		if block.Type == "text" {
			ar.Content = append(ar.Content, ToolContent{Type: "text", Text: block.Text})
		}
	}
	return ar
}

func parseTaskEvent(raw *rawEvent, line []byte) *TaskEvent {
	status := ""
	if raw.Status != nil {
		status = *raw.Status
	}
	return &TaskEvent{
		Subtype:      raw.Subtype,
		TaskID:       raw.TaskID,
		ToolUseID:    raw.ToolUseID,
		SessionID:    raw.SessionID,
		Description:  raw.Description,
		TaskType:     raw.TaskType,
		Prompt:       raw.Prompt,
		LastToolName: raw.LastToolName,
		Status:       status,
		Summary:      raw.Summary,
		TotalTokens:  raw.Usage.TotalTokens,
		ToolUses:     raw.Usage.ToolUses,
		DurationMs:   raw.Usage.DurationMs,
		Raw:          append(json.RawMessage(nil), line...),
	}
}
