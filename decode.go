package claudecli

import "encoding/json"

// decodeStatelessEvent decodes the wire types whose translation into an Event
// depends on nothing but the line itself.
//
// The library runs two decode loops over the same wire format: ParseEvents for
// one-shot `-p` streams, and Session.readLoop for Connect() sessions. Only a
// handful of types need loop-local state — "init" (session id, ready latch),
// "assistant" and "result" (accumulated result text and context snapshot),
// "stream_event" (snapshot updates), "error" (last-error tracking), and the
// control frames. Everything else decodes identically in both, and previously
// had to be written twice; a case added to one loop but not the other silently
// worked in `-p` mode and not in Connect(), or vice versa.
//
// Returning (nil, false) means "not mine" — the caller falls through to its own
// switch. Returning (nil, true) means "handled, emit nothing" (keep_alive).
// Unknown system subtypes ARE handled here (as UnknownEvent) so that a new CLI
// subtype surfaces identically in both loops.
func decodeStatelessEvent(raw *rawEvent, line []byte, backfill *taskTypeBackfiller) (Event, bool) {
	switch raw.Type {
	case "system":
		switch raw.Subtype {
		case "init", "":
			// Stateful: the caller latches session id and readiness.
			return nil, false

		case "status":
			status := ""
			if raw.Status != nil {
				status = *raw.Status
			}
			return &CompactStatusEvent{SessionID: raw.SessionID, Status: status}, true

		case "compact_boundary":
			return parseCompactBoundaryEvent(raw), true

		case "task_started", "task_progress", "task_updated", "task_notification":
			return backfill.apply(parseTaskEvent(raw, line)), true

		case "hook_started", "hook_progress", "hook_response":
			return parseHookEvent(raw, line), true

		case "thinking_tokens":
			return &ThinkingTokensEvent{
				EstimatedTokens:      raw.EstimatedTokens,
				EstimatedTokensDelta: raw.EstimatedTokensDelta,
				SessionID:            raw.SessionID,
				UUID:                 raw.UUID,
			}, true

		case "files_persisted":
			return parseFilesPersistedEvent(raw), true

		case "background_tasks_changed":
			return parseBackgroundTasksChangedEvent(raw), true

		case "session_state_changed":
			return parseSessionStateChangedEvent(raw), true

		case "permission_denied":
			return parsePermissionDeniedEvent(raw, line), true

		default:
			return &UnknownEvent{
				Type: "system/" + raw.Subtype,
				Raw:  append(json.RawMessage(nil), line...),
			}, true
		}

	case "rate_limit_event":
		return parseRateLimitEvent(raw), true

	case "user":
		return parseUserEvent(raw), true

	case "prompt_suggestion":
		return &PromptSuggestionEvent{
			Suggestion: raw.Suggestion,
			SessionID:  raw.SessionID,
			UUID:       raw.UUID,
		}, true

	case "tool_progress":
		return parseCLIToolProgressEvent(raw), true

	case "tool_use_summary":
		return parseToolUseSummaryEvent(raw), true

	case "auth_status":
		return parseAuthStatusEvent(raw), true

	case "conversation_reset":
		return parseConversationResetEvent(raw), true

	case "keep_alive":
		// Liveness heartbeat with no payload; the CLI emits it periodically,
		// for example while a long control request is in flight. The protocol
		// requires receivers to ignore it — surfacing it as an UnknownEvent
		// would read as a parse failure. Handled, deliberately not emitted.
		return nil, true
	}

	return nil, false
}
