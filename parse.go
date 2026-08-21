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

// previewLine returns a truncated, single-line representation of a raw
// JSONL line for inclusion in error messages. Caps output at 200 bytes
// and collapses embedded newlines so log lines stay scannable.
func previewLine(line []byte) string {
	const max = 200
	s := string(line)
	if len(s) > max {
		s = s[:max] + "...(truncated)"
	}
	// Collapse newlines (shouldn't appear inside a JSONL line, but be safe).
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}

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
	var turnCounter int
	taskBackfill := newTaskTypeBackfiller()

	tracker := newActivityTracker()
	// emit wraps ch-send with activity tracking: a CLIStateChangeEvent is
	// emitted BEFORE ev when the tracker detects a transition.
	emit := func(ev Event) {
		if transition := tracker.observe(ev); transition != nil {
			ch <- transition
		}
		ch <- ev
	}

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
			emit(&ErrorEvent{Err: fmt.Errorf("unmarshal JSONL: %w (line: %s)", err, previewLine(line))})
			continue
		}

		if ev, ok := decodeStatelessEvent(&raw, line, taskBackfill); ok {
			emit(ev)
			continue
		}

		switch raw.Type {
		case "system":
			// decodeStatelessEvent handles every subtype but init.
			emit(parseInitEvent(&raw))

		case "assistant":
			if raw.Message == nil {
				continue
			}
			// claude-cli emits a synthetic assistant message when the
			// upstream Anthropic stream drops mid-turn: model="<synthetic>",
			// isApiErrorMessage=true, content=[{type:"text", text:"API
			// Error: ..."}]. Forwarding that text as a real reply leaks the
			// transport error into the application (Neo discord-agent
			// incident 2026-05-22). Emit as fatal ErrorEvent and terminate
			// the stream — caller's session goes to StateFailed and next
			// query reconnects with a fresh session.
			if raw.IsApiErrorMessage {
				msg := ""
				for _, block := range raw.Message.Content {
					if block.Type == "text" {
						msg += block.Text
					}
				}
				if msg == "" {
					msg = "synthetic CLI api-error message"
				}
				emit(&ErrorEvent{
					Err:   fmt.Errorf("%w: %s", ErrAPI, msg),
					Fatal: true,
				})
				return
			}
			parentToolUseID := ""
			if raw.ParentToolUseID != nil {
				parentToolUseID = *raw.ParentToolUseID
			}
			// Emit TurnEvent for top-level assistant messages only
			if parentToolUseID == "" {
				turnCounter++
				toolName := ""
				for _, block := range raw.Message.Content {
					if block.Type == "tool_use" {
						toolName = block.Name
						break
					}
				}
				emit(&TurnEvent{Turn: turnCounter, ToolName: toolName})
			}
			meta := assistantMeta{
				ParentToolUseID: parentToolUseID,
				Model:           raw.Message.Model,
				SubagentType:    raw.SubagentType,
				TaskDescription: raw.TaskDescription,
			}
			for _, block := range raw.Message.Content {
				parseContentBlock(block, meta, &resultText, emit)
			}
			if len(raw.Message.ContextManagement) > 0 && string(raw.Message.ContextManagement) != "null" {
				emit(&ContextManagementEvent{Raw: raw.Message.ContextManagement})
			}

		case "result":
			modelUsage := convertModelUsage(raw.ModelUsage)
			if snapshot != nil && lastModel != "" {
				if mu, ok := lookupModelUsage(modelUsage, lastModel); ok {
					snapshot.ContextWindow = mu.ContextWindow
				}
			}
			// Classify error_max_turns: emit a non-fatal ErrorEvent so callers
			// using errors.Is(err, ErrMaxTurns) can detect it via Stream.Wait().
			if raw.Subtype == "error_max_turns" {
				mte := classifyMaxTurns(raw.Errors)
				emit(&ErrorEvent{Err: mte, Fatal: false})
			}
			emit(&ResultEvent{
				Text:             strings.Join(resultText, ""),
				Subtype:          raw.Subtype,
				StopReason:       raw.StopReason,
				StructuredOutput: raw.StructuredOutput,
				Duration:         time.Duration(raw.DurationMS) * time.Millisecond,
				CostUSD:          raw.CostUSD,
				SessionID:        raw.SessionID,
				NumTurns:         raw.NumTurns,
				Usage:            raw.Usage.toUsage(),
				ModelUsage:       modelUsage,
				ContextSnapshot:  snapshot,
			})
			// Result is the terminal event. Return immediately to avoid
			// blocking on scanner.Scan() if the CLI keeps stdout open (known bug).
			return

		case "control_request":
			var body rawControlRequestBody
			if err := json.Unmarshal(raw.Request, &body); err != nil {
				emit(&ErrorEvent{Err: fmt.Errorf("unmarshal control request: %w", err)})
				continue
			}
			emit(&ControlRequestEvent{
				RequestID: raw.RequestID,
				Subtype:   body.Subtype,
				Body:      raw.Request,
			})

		case "stream_event":
			emit(&StreamEvent{
				UUID:      raw.UUID,
				SessionID: raw.SessionID,
				Event:     raw.Event,
			})
			updateContextSnapshot(raw.Event, &snapshot, &lastModel)

		case "error":
			emit(parseErrorEvent(&raw))

		default:
			emit(&UnknownEvent{
				Type: raw.Type,
				Raw:  append(json.RawMessage(nil), line...),
			})
		}
	}

	if err := scanner.Err(); err != nil {
		emit(&ErrorEvent{Err: fmt.Errorf("scanner: %w", err)})
	}
}

// assistantMeta carries the wrapper-level fields of an assistant message down
// to the per-block events it decodes into. The CLI reports the model, subagent
// type and task description once on the message, not on each content block.
type assistantMeta struct {
	ParentToolUseID string
	Model           string
	SubagentType    string
	TaskDescription string
}

func parseContentBlock(block rawContent, meta assistantMeta, resultText *[]string, emit func(Event)) {
	switch block.Type {
	case "thinking":
		emit(&ThinkingEvent{
			Content:         block.Thinking,
			Signature:       block.Signature,
			ParentToolUseID: meta.ParentToolUseID,
			Model:           meta.Model,
			SubagentType:    meta.SubagentType,
			TaskDescription: meta.TaskDescription,
		})
	case "text":
		*resultText = append(*resultText, block.Text)
		emit(&TextEvent{
			Content:         block.Text,
			ParentToolUseID: meta.ParentToolUseID,
			Model:           meta.Model,
			SubagentType:    meta.SubagentType,
			TaskDescription: meta.TaskDescription,
		})
	case "tool_use":
		emit(&ToolUseEvent{
			ID:              block.ID,
			Name:            block.Name,
			Input:           block.Input,
			ParentToolUseID: meta.ParentToolUseID,
			Model:           meta.Model,
			SubagentType:    meta.SubagentType,
			TaskDescription: meta.TaskDescription,
		})
	case "server_tool_use":
		emit(&ToolUseEvent{
			ID:              block.ID,
			Name:            block.Name,
			Input:           block.Input,
			ParentToolUseID: meta.ParentToolUseID,
			Model:           meta.Model,
			SubagentType:    meta.SubagentType,
			TaskDescription: meta.TaskDescription,
			ServerSide:      true,
		})
	case "mcp_tool_use":
		emit(&ToolUseEvent{
			ID:              block.ID,
			Name:            block.Name,
			Input:           block.Input,
			ParentToolUseID: meta.ParentToolUseID,
			Model:           meta.Model,
			SubagentType:    meta.SubagentType,
			TaskDescription: meta.TaskDescription,
			MCP:             true,
		})
	case "tool_result":
		emit(&ToolResultEvent{
			ToolUseID:       block.ToolUseID,
			Content:         extractContent(block.Content),
			ParentToolUseID: meta.ParentToolUseID,
		})
	default:
		if block.Type != "" {
			raw, _ := json.Marshal(block)
			emit(&UnknownEvent{
				Type: "content/" + block.Type,
				Raw:  raw,
			})
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
	var unmarshalFailed bool
	if len(raw.ErrorData) > 0 {
		if err := json.Unmarshal(raw.ErrorData, &errObj); err != nil {
			unmarshalFailed = true
		}
	}

	msg := errObj.Message
	if msg == "" {
		if unmarshalFailed {
			msg = "unknown error (unmarshal failed: " + previewLine(raw.ErrorData) + ")"
		} else {
			msg = "unknown error"
		}
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
	SessionID       string            `json:"session_id,omitempty"`
	Model           string            `json:"model,omitempty"`
	Tools           []string          `json:"tools,omitempty"`
	Agents          []string          `json:"agents,omitempty"`
	Skills          []string          `json:"skills,omitempty"`
	MCPServers      []MCPServerStatus `json:"mcp_servers,omitempty"`
	MCPServerErrors []MCPServerError  `json:"mcp_server_errors,omitempty"`
	CLIVersion      string            `json:"claude_code_version,omitempty"`
	CWD             string            `json:"cwd,omitempty"`
	PermissionMode  string            `json:"permissionMode,omitempty"`
	OutputStyle     string            `json:"output_style,omitempty"`
	SlashCommands   []string          `json:"slash_commands,omitempty"`
	Plugins         []PluginInfo      `json:"plugins,omitempty"`

	// prompt_suggestion event
	Suggestion string `json:"suggestion,omitempty"`

	// system event (status subtype)
	Status *string `json:"status"`

	// system event (compact_boundary subtype)
	CompactMetadata json.RawMessage `json:"compact_metadata,omitempty"`

	// system task subtypes (task_started, task_progress, task_updated, task_notification)
	TaskID           string                  `json:"task_id,omitempty"`
	ToolUseID        string                  `json:"tool_use_id,omitempty"`
	Description      string                  `json:"description,omitempty"`
	TaskType         string                  `json:"task_type,omitempty"`
	Prompt           string                  `json:"prompt,omitempty"`
	LastToolName     string                  `json:"last_tool_name,omitempty"`
	Summary          string                  `json:"summary,omitempty"`
	WorkflowName     string                  `json:"workflow_name,omitempty"`
	OutputFile       string                  `json:"output_file,omitempty"`
	WorkflowProgress []WorkflowProgressEntry `json:"workflow_progress,omitempty"`
	Patch            json.RawMessage         `json:"patch,omitempty"`

	// system subtype thinking_tokens
	EstimatedTokens      int `json:"estimated_tokens,omitempty"`
	EstimatedTokensDelta int `json:"estimated_tokens_delta,omitempty"`

	// system hook subtypes (hook_started, hook_response)
	HookID    string `json:"hook_id,omitempty"`
	HookName  string `json:"hook_name,omitempty"`
	HookEvent string `json:"hook_event,omitempty"`
	Output    string `json:"output,omitempty"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	ExitCode  int    `json:"exit_code,omitempty"`
	Outcome   string `json:"outcome,omitempty"`

	// assistant + user events
	Message *rawMessage `json:"message,omitempty"`
	// SubagentType is set on assistant messages produced by a subagent, and
	// on the task_* system subtypes.
	SubagentType string `json:"subagent_type,omitempty"`
	// TaskDescription describes the subagent task that produced an assistant
	// message. Distinct from the task events' "description" field.
	TaskDescription string          `json:"task_description,omitempty"`
	ParentToolUseID *string         `json:"parent_tool_use_id,omitempty"`
	Timestamp       string          `json:"timestamp,omitempty"`
	ToolUseResult   json.RawMessage `json:"tool_use_result,omitempty"`
	IsReplay        bool            `json:"isReplay,omitempty"`

	// Set on synthetic assistant messages that claude-cli emits when the
	// upstream Anthropic stream drops mid-turn. The accompanying content
	// is the error text rendered as if the model wrote it, with
	// "model":"<synthetic>". Used to discriminate transport errors from
	// real model output — see assistant-case in ParseEvents.
	IsApiErrorMessage bool `json:"isApiErrorMessage,omitempty"`

	// result event
	Result           string                   `json:"result,omitempty"`
	DurationMS       float64                  `json:"duration_ms,omitempty"`
	CostUSD          float64                  `json:"total_cost_usd,omitempty"`
	StopReason       string                   `json:"stop_reason,omitempty"`
	StructuredOutput json.RawMessage          `json:"structured_output,omitempty"`
	NumTurns         int                      `json:"num_turns,omitempty"`
	IsError          bool                     `json:"is_error,omitempty"`
	TerminalReason   string                   `json:"terminal_reason,omitempty"`
	Errors           []string                 `json:"errors,omitempty"`
	Usage            rawUsage                 `json:"usage,omitempty"`
	ModelUsage       map[string]rawModelUsage `json:"modelUsage,omitempty"`

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

	// tool_progress (top-level)
	ToolName           string  `json:"tool_name,omitempty"`
	ElapsedTimeSeconds float64 `json:"elapsed_time_seconds,omitempty"`

	// tool_use_summary (top-level)
	PrecedingToolUseIDs []string `json:"preceding_tool_use_ids,omitempty"`

	// auth_status (top-level)
	IsAuthenticating bool `json:"isAuthenticating,omitempty"`

	// files_persisted (system subtype)
	Files  json.RawMessage `json:"files,omitempty"`
	Failed json.RawMessage `json:"failed,omitempty"`
}

type rawMessage struct {
	Content           rawFlexContent  `json:"content"`
	ContextManagement json.RawMessage `json:"context_management,omitempty"`
	// Model is the API model that produced this message. On subagent
	// messages (ParentToolUseID set) this is the resolved id the subagent
	// actually ran on, which is strictly better than the alias in the
	// spawning Agent tool's input — and is the only place it appears.
	Model string `json:"model,omitempty"`
}

// UnmarshalJSON accepts both the canonical object form
// ({"role":"user","content":[...]}) and a bare string form
// ("some text"), converting the latter to a single text content block.
// Mirrors the rawFlexContent precedent below — the CLI has been
// observed (after agent_result events with status="async_launched")
// emitting a top-level "message":"<string>" instead of the usual
// object, which would otherwise fail the whole-line unmarshal and
// drop the event.
func (m *rawMessage) UnmarshalJSON(data []byte) error {
	// Try the canonical object form first.
	type alias rawMessage
	var obj alias
	objErr := json.Unmarshal(data, &obj)
	if objErr == nil {
		*m = rawMessage(obj)
		return nil
	}
	// Fall back to plain string.
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*m = rawMessage{Content: []rawContent{{Type: "text", Text: s}}}
		return nil
	}
	// Neither shape matched; surface the original object-form error so
	// the line preview in the pump's ErrorEvent carries a useful hint.
	return objErr
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
		Model string        `json:"model"`
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
		// A dynamic workflow launch and an ordinary subagent result share the
		// tool_use_result slot. Disambiguate on the launch markers before
		// falling back to AgentResult parsing.
		if wl := parseWorkflowLaunch(raw.ToolUseResult); wl != nil {
			ev.WorkflowLaunch = wl
		} else {
			ev.AgentResult = parseAgentResult(raw.ToolUseResult)
		}
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

// parseInitEvent builds an InitEvent from a system/init line. Shared by
// ParseEvents and the Session stdout pump so both surface the same fields.
func parseInitEvent(raw *rawEvent) *InitEvent {
	return &InitEvent{
		SessionID:       raw.SessionID,
		Model:           raw.Model,
		Tools:           raw.Tools,
		Agents:          raw.Agents,
		Skills:          raw.Skills,
		MCPServers:      raw.MCPServers,
		MCPServerErrors: raw.MCPServerErrors,
		CLIVersion:      raw.CLIVersion,
		CWD:             raw.CWD,
		PermissionMode:  PermissionMode(raw.PermissionMode),
		OutputStyle:     raw.OutputStyle,
		SlashCommands:   raw.SlashCommands,
		Plugins:         raw.Plugins,
	}
}

func parseHookEvent(raw *rawEvent, line []byte) *HookEvent {
	return &HookEvent{
		Subtype:   raw.Subtype,
		HookID:    raw.HookID,
		HookName:  raw.HookName,
		HookEvent: raw.HookEvent,
		UUID:      raw.UUID,
		SessionID: raw.SessionID,
		Output:    raw.Output,
		Stdout:    raw.Stdout,
		Stderr:    raw.Stderr,
		ExitCode:  raw.ExitCode,
		Outcome:   raw.Outcome,
		Raw:       append(json.RawMessage(nil), line...),
	}
}

func parseTaskEvent(raw *rawEvent, line []byte) *TaskEvent {
	status := ""
	if raw.Status != nil {
		status = *raw.Status
	}
	var endTime int64
	// task_updated carries its status (and an end_time) inside a "patch"
	// object rather than as top-level fields.
	if len(raw.Patch) > 0 {
		var patch struct {
			Status  *string `json:"status"`
			EndTime int64   `json:"end_time"`
		}
		if json.Unmarshal(raw.Patch, &patch) == nil {
			if patch.Status != nil {
				status = *patch.Status
			}
			endTime = patch.EndTime
		}
	}
	return &TaskEvent{
		Subtype:          raw.Subtype,
		TaskID:           raw.TaskID,
		ToolUseID:        raw.ToolUseID,
		SessionID:        raw.SessionID,
		Description:      raw.Description,
		SubagentType:     raw.SubagentType,
		TaskType:         raw.TaskType,
		Prompt:           raw.Prompt,
		WorkflowName:     raw.WorkflowName,
		LastToolName:     raw.LastToolName,
		WorkflowProgress: raw.WorkflowProgress,
		Status:           status,
		Summary:          raw.Summary,
		OutputFile:       raw.OutputFile,
		EndTime:          endTime,
		TotalTokens:      raw.Usage.TotalTokens,
		ToolUses:         raw.Usage.ToolUses,
		DurationMs:       raw.Usage.DurationMs,
		Raw:              append(json.RawMessage(nil), line...),
	}
}

func parseCLIToolProgressEvent(raw *rawEvent) *CLIToolProgressEvent {
	return &CLIToolProgressEvent{
		ToolUseID:      raw.ToolUseID,
		ToolName:       raw.ToolName,
		ElapsedSeconds: raw.ElapsedTimeSeconds,
		TaskID:         raw.TaskID,
	}
}

func parseToolUseSummaryEvent(raw *rawEvent) *ToolUseSummaryEvent {
	return &ToolUseSummaryEvent{
		Summary:             raw.Summary,
		PrecedingToolUseIDs: raw.PrecedingToolUseIDs,
	}
}

func parseAuthStatusEvent(raw *rawEvent) *AuthStatusEvent {
	ev := &AuthStatusEvent{
		IsAuthenticating: raw.IsAuthenticating,
		Output:           raw.Output,
	}
	if len(raw.ErrorData) > 0 {
		var errStr string
		if json.Unmarshal(raw.ErrorData, &errStr) == nil {
			ev.Error = errStr
		}
	}
	return ev
}

func parseFilesPersistedEvent(raw *rawEvent) *FilesPersistedEvent {
	ev := &FilesPersistedEvent{}
	if len(raw.Files) > 0 {
		var files []struct {
			Filename string `json:"filename"`
			FileID   string `json:"file_id"`
		}
		if json.Unmarshal(raw.Files, &files) == nil {
			for _, f := range files {
				ev.Files = append(ev.Files, PersistedFile{Filename: f.Filename, FileID: f.FileID})
			}
		}
	}
	if len(raw.Failed) > 0 {
		var failed []struct {
			Filename string `json:"filename"`
			Error    string `json:"error"`
		}
		if json.Unmarshal(raw.Failed, &failed) == nil {
			for _, f := range failed {
				ev.Failed = append(ev.Failed, FailedFile{Filename: f.Filename, Error: f.Error})
			}
		}
	}
	return ev
}
