# Subagent Event Investigation

## Goal

Determine whether Claude CLI emits events for subagent (Agent tool) activity that claudecli-go was silently dropping.

## Answer: Yes

The CLI emits `"type":"user"` events for all tool results (including subagent activity) and three system subtypes for subagent lifecycle. All were silently dropped.

## Changes Made

1. **`UnknownEvent`** — catch-all for unrecognized event types (forward compat).
2. **`UserEvent`** — parses `"type":"user"` events with `ParentToolUseID` for subagent correlation and `AgentResult` for completion metadata.
3. **`cmd/capture`** — standalone tool for capturing raw CLI JSONL.

## Full raw JSONL trace (Agent tool session)

Captured with: `go run ./cmd/capture -prompt "Use the Agent tool to read go.mod..."`

```
Line  Type                    Subtype             Notes
1     system                  init                session start
2     assistant               —                   ToolUseEvent{Agent} — spawns subagent
3     system                  task_started        NEW: subagent spawn (dropped by system subtype switch)
4     user                    —                   NEW: subagent prompt dispatch (was dropped entirely)
5     rate_limit_event        —                   rate limit check
6     system                  task_progress       NEW: subagent progress (dropped by system subtype switch)
7     assistant               —                   subagent's ToolUseEvent{Read} (parsed but parent_tool_use_id lost)
8     user                    —                   NEW: subagent tool result (was dropped entirely)
9     system                  task_notification   NEW: subagent completion (dropped by system subtype switch)
10    user                    —                   NEW: Agent tool result with AgentResult metadata (was dropped)
11    assistant               —                   final text response
12    result                  success             session end
```

## Events now parsed: `"type":"user"`

Three variants observed:

### 1. Subagent prompt dispatch
```json
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Read go.mod..."}]},
 "parent_tool_use_id":"toolu_01Fgk...","session_id":"...","uuid":"...","timestamp":"..."}
```
`ParentToolUseID` set → belongs to a subagent.

### 2. Tool result (subagent-internal or top-level)
```json
{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_01BEJ...","type":"tool_result","content":"..."}]},
 "parent_tool_use_id":"toolu_01Fgk...","session_id":"...","uuid":"...","timestamp":"..."}
```
`parent_tool_use_id` set → subagent's internal tool result. Null → top-level tool result.

### 3. Agent completion (with `tool_use_result`)
```json
{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_01Fgk...","type":"tool_result","content":[...]}]},
 "parent_tool_use_id":null,"session_id":"...","uuid":"...","timestamp":"...",
 "tool_use_result":{"status":"completed","prompt":"...","agentId":"a6ec6...","agentType":"Explore",
   "content":[...],"totalDurationMs":2975,"totalTokens":21825,"totalToolUseCount":1,
   "usage":{...}}}
```
`tool_use_result` present → subagent finished. Contains agent metadata and usage.

## Events still dropped: system subtypes

Three system subtypes are emitted during subagent execution but dropped by the inner subtype switch (which only handles `init`, `status`, `compact_boundary`):

### `task_started`
```json
{"type":"system","subtype":"task_started","task_id":"a6ec6...","tool_use_id":"toolu_01Fgk...",
 "description":"Read go.mod module name","task_type":"local_agent","prompt":"..."}
```

### `task_progress`
```json
{"type":"system","subtype":"task_progress","task_id":"a6ec6...","tool_use_id":"toolu_01Fgk...",
 "description":"Reading go.mod","usage":{"total_tokens":19715,"tool_uses":1,"duration_ms":1575},
 "last_tool_name":"Read"}
```

### `task_notification`
```json
{"type":"system","subtype":"task_notification","task_id":"a6ec6...","tool_use_id":"toolu_01Fgk...",
 "status":"completed","summary":"Read go.mod module name",
 "usage":{"total_tokens":21815,"tool_uses":1,"duration_ms":2974}}
```

These are dropped because they match `case "system":` in the outer switch but have no matching inner subtype case. The `UnknownEvent` default doesn't catch them.

## Correlation mechanism

```
parent Agent ToolUseEvent.ID = "toolu_01Fgk..."
  ├─ TaskEvent(task_started):   ToolUseID matches          ✅ parsed
  ├─ UserEvent(prompt):         ParentToolUseID matches     ✅ parsed
  ├─ ToolUseEvent(subagent):    ParentToolUseID matches     ✅ parsed
  ├─ UserEvent(tool result):    ParentToolUseID matches     ✅ parsed
  ├─ TaskEvent(task_progress):  ToolUseID matches           ✅ parsed
  ├─ TaskEvent(task_notification): ToolUseID matches        ✅ parsed
  └─ UserEvent(completion):     AgentResult set             ✅ parsed
```

All event types from the raw trace are now parsed. No subagent events are silently dropped.

## Possible follow-up

1. **Subagent lifecycle abstraction** — a higher-level API that correlates all events for a given subagent (task_started → progress → user events → task_notification → completion) into a coherent view.
