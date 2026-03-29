# Subagent Event Investigation

## Goal

Determine whether Claude CLI emits events for subagent (Agent tool) activity that claudecli-go was silently dropping.

## Changes Made

1. **UnknownEvent type** (`event.go`) — new event carrying `Type` string + `Raw` JSON for any unrecognized event type.
2. **Default cases** (`parse.go`, `session.go`) — both event parsing switches now emit `UnknownEvent` instead of silently dropping.
3. **Capture tool** (`cmd/capture/main.go`) — standalone binary that captures raw CLI stdout/stderr during a session that triggers the Agent tool.

## How to Run

```bash
# Capture raw CLI output (requires claude CLI + API key):
go run ./cmd/capture -prompt "Use the Agent tool to read go.mod and tell me the module name"

# Analyze a previously captured JSONL file:
go run ./cmd/capture -analyze tmp/raw-stdout.jsonl
```

Output files:
- `tmp/raw-stdout.jsonl` — raw JSONL from CLI stdout
- `tmp/raw-stderr.log` — raw stderr from CLI process

## Findings

> **Run the capture tool and fill in results below.**

### Event types observed

_Paste event type summary from capture output._

### Unknown/unrecognized events

_List any UnknownEvent instances with their type and raw JSON._

### Subagent event structure

Questions to answer from the raw JSONL:

- Are there event types we don't recognize? (e.g., `subagent_start`, `subagent_progress`, `subagent_end`)
- Do subagent events have a different structure? (e.g., nested messages, an `agent_id` field)
- Is there a way to correlate subagent events with the parent Agent tool call? (e.g., matching tool_use_id)
- What information is available? (tool calls, text output, thinking, intermediate results)
- Does `--verbose` affect what events appear for subagents?

### Hypothesis

Most likely outcome: the CLI does NOT emit separate event types for subagent activity. Subagent execution is opaque — we see:
1. `ToolUseEvent` with `Name: "Agent"` (the invocation)
2. Eventually a `ToolResultEvent` with the subagent's final output

If this is confirmed, the only way to get subagent visibility would be to request it from Anthropic as a CLI feature.

Alternative: the CLI might emit the subagent's events inline in the same stream, but with some marker (agent_id field, nested type). The UnknownEvent + capture tool will detect this.

## Recommendations

_Fill in after analysis._
