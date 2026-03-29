# Subagent Event Investigation

## Goal

Determine whether Claude CLI emits events for subagent (Agent tool) activity that claudecli-go was silently dropping.

## Changes Made

1. **UnknownEvent type** (`event.go`) — new event carrying `Type` string + `Raw` JSON for any unrecognized event type.
2. **Default cases** (`parse.go`, `session.go`) — both event parsing switches now emit `UnknownEvent` instead of silently dropping.
3. **Capture tool** (`cmd/capture/main.go`) — standalone binary that captures raw CLI stdout/stderr during a session that triggers the Agent tool.

## Findings

### The CLI emits `"type":"user"` events we were silently dropping

The CLI emits events with `"type":"user"` for every tool result message sent back to the model. These were being silently dropped by both `ParseEvents()` and session `readLoop()`.

### Three kinds of `user` events observed during Agent tool execution

**1. Subagent prompt dispatch** — when the Agent tool starts a subagent:
```json
{
  "type": "user",
  "message": {
    "role": "user",
    "content": [{"type": "text", "text": "Read the file go.mod and report the module name."}]
  },
  "parent_tool_use_id": "toolu_01FgkLdcGjWy6wWGZyaBDsz7",
  "session_id": "...",
  "uuid": "...",
  "timestamp": "2026-03-29T18:36:53.939Z"
}
```

**2. Subagent internal tool results** — tool results within the subagent's execution:
```json
{
  "type": "user",
  "message": {
    "role": "user",
    "content": [{"tool_use_id": "toolu_01BEJwRB6DJxXjn9Jc58eEoR", "type": "tool_result", "content": "1\tmodule github.com/allbin/claudecli-go\n2\t..."}]
  },
  "parent_tool_use_id": "toolu_01FgkLdcGjWy6wWGZyaBDsz7",
  "session_id": "...",
  "uuid": "...",
  "timestamp": "2026-03-29T18:36:55.519Z"
}
```

**3. Subagent completion** — final result returned to the parent, with rich metadata:
```json
{
  "type": "user",
  "message": {
    "role": "user",
    "content": [{"tool_use_id": "toolu_01FgkLdcGjWy6wWGZyaBDsz7", "type": "tool_result", "content": [...]}]
  },
  "parent_tool_use_id": null,
  "session_id": "...",
  "uuid": "...",
  "timestamp": "2026-03-29T18:36:56.915Z",
  "tool_use_result": {
    "status": "completed",
    "prompt": "Read the file go.mod and report the module name.",
    "agentId": "a6ec69258506eec3a",
    "agentType": "Explore",
    "content": [{"type": "text", "text": "The module name in the go.mod file is **github.com/allbin/claudecli-go**."}],
    "totalDurationMs": 2975,
    "totalTokens": 21825,
    "totalToolUseCount": 1,
    "usage": {
      "input_tokens": 5,
      "cache_creation_input_tokens": 2097,
      "cache_read_input_tokens": 19696,
      "output_tokens": 27,
      "server_tool_use": {"web_search_requests": 0, "web_fetch_requests": 0},
      "service_tier": "standard"
    }
  }
}
```

### Key structural observations

| Field | Meaning |
|-------|---------|
| `parent_tool_use_id` | Links subagent events to the parent Agent `ToolUseEvent.ID`. Non-null = subagent activity. Null = top-level tool result. |
| `tool_use_result` | Present only on the final completion event. Contains `agentId`, `agentType`, `totalDurationMs`, `totalTokens`, `totalToolUseCount`, full `usage` breakdown. |
| `message.content[].tool_use_id` | On completion, matches the parent `ToolUseEvent.ID` — this is the tool_result that closes the Agent invocation. |

### What's NOT emitted

- No separate `"type":"assistant"` events for the subagent's thinking or text output. The subagent's intermediate assistant turns are invisible.
- No `ToolUseEvent` for the subagent's internal tool calls — only the `user` event carrying the tool_result is emitted.
- No dedicated event types like `subagent_start` or `subagent_end`.

### Correlation mechanism

`parent_tool_use_id` is the correlation key:
- Events where `parent_tool_use_id == <Agent ToolUseEvent.ID>` are subagent activity
- The final event where `message.content[].tool_use_id == <Agent ToolUseEvent.ID>` and `parent_tool_use_id == null` is the completion

### Also applies to non-Agent tool results

The `"type":"user"` event is emitted for ALL tool results, not just Agent. In the non-Agent run, a simple `Read` tool result also appeared as a `user` event. This means we've been missing tool result feedback for every tool call.

## Event type summary (Agent tool session)

```
*claudecli.InitEvent                1
*claudecli.RateLimitEvent           1
*claudecli.ResultEvent              1
*claudecli.TextEvent                1
*claudecli.ToolUseEvent             2   (Agent + subagent's Read)
*claudecli.UnknownEvent             3   (all "type":"user" events)
```

## Recommendations

1. **Add `UserEvent` type** to parse `"type":"user"` events. This is a real event type the CLI emits — not forward-compat speculation. Structure:
   - `Message` with role + content blocks (reuse existing content parsing)
   - `ParentToolUseID` for subagent correlation
   - `ToolUseResult` for Agent completion metadata (agentId, agentType, usage, etc.)
   - `UUID`, `SessionID`, `Timestamp`

2. **Subagent tracking** becomes possible with `parent_tool_use_id`:
   - When a `UserEvent` has `parent_tool_use_id != ""`, it's subagent activity
   - When `tool_use_result` is present, the subagent has completed
   - `agentId` + `agentType` identify the subagent

3. **Non-subagent tool results** are also now visible. The `UserEvent` with `parent_tool_use_id == null` and a `tool_use_id` in its content carries the tool result that was fed back to the model. This closes the loop: `ToolUseEvent` → tool executes → `UserEvent` with result → model continues.

4. **The `UnknownEvent` infra remains useful** as a catch-all for future CLI event types.
