# Dynamic workflows over the CLI — findings (2026-06-29)

Empirical investigation of how Claude Code **dynamic workflows**
(<https://code.claude.com/docs/en/workflows>) behave through
`claude -p --output-format stream-json` — the surface `claudecli-go`
wraps. Captured against CLI **v2.1.186** using `cmd/capture` plus raw
`claude` invocations. Two runs: a bundled `/deep-research` workflow and a
minimal single-agent `ultracode` workflow.

This document records the protocol facts; the implementation that follows
from them lives in `event.go`, `parse.go`, and `workflow.go`.

## TL;DR

- Workflows are a **CLI-runtime feature**, not an SDK protocol layer. The
  Python SDK has zero workflow surface. Triggering needs no new flags —
  it is pure prompt content (`ultracode: …`, `/deep-research …`, a saved
  `/<name>`). `--dangerously-skip-permissions` makes it auto-run headless.
- A workflow surfaces through the **existing** `task_started` /
  `task_progress` / `task_notification` system events, so it was already
  parsed into `TaskEvent` before this change — nothing became
  `UnknownEvent` for the lifecycle itself.
- The run is a **two-turn lifecycle** and emits **two `result` events**;
  the first is "running in the background", the second is the real
  answer. Consumers must take the **last** `ResultEvent`.
- The workflow runtime persists a **live, structured run state on disk**,
  keyed by `runId`, and it survives `--no-session-persistence` (the flag
  the SDK sets). This enables out-of-band monitoring.

## Lifecycle (headless `-p`, stream-json)

```
Turn 1:  assistant calls the Workflow tool
         user/tool_result: status:"async_launched"
              { taskId, taskType:"local_workflow", workflowName, runId,
                summary, transcriptDir, scriptPath }
         result #1: "running in the background, I'll report when it completes"   ← NOT the answer
   …     process stays alive, streaming:
         system/task_started      task_type:"local_workflow", workflow_name,
                                   prompt = the entire workflow JS script
         system/task_progress ×N  carries workflow_progress[] (per-agent state)
         system/task_updated       { patch:{ status, end_time } }
         system/task_notification  status:"completed", output_file:<path>, usage
Turn 2:  assistant emits the real answer
         result #2: "Done … DONE-MARKER-42"                                     ← the answer
         process exits 0
```

A workflow **does not survive the `-p` process** — killing the process
sets `status:"killed"` (observed when our 480 s timeout cut the
`/deep-research` run off mid-Verify). The on-disk files persist for
inspection, but the run does not keep progressing.

## Event shapes

### `task_started` (new fields beyond what `TaskEvent` modeled)
```json
{ "type":"system", "subtype":"task_started", "task_id":"w8a0hi7jg",
  "tool_use_id":"toolu_…", "task_type":"local_workflow",
  "workflow_name":"deep-research",
  "description":"…", "prompt":"export const meta = { … }  // full JS script" }
```

### `task_progress` — carries `workflow_progress[]`
```json
{ "type":"system", "subtype":"task_progress", "task_id":"w8a0hi7jg",
  "usage":{ "total_tokens":408651, "tool_uses":114, "duration_ms":407148 },
  "workflow_progress":[
    { "type":"workflow_agent", "index":38, "label":"v1:…", "phaseIndex":4,
      "phaseTitle":"Verify", "agentId":"ac9d1b08922c0ac23",
      "model":"claude-opus-4-8[1m]", "state":"progress", "attempt":1,
      "lastToolName":"StructuredOutput", "lastToolSummary":"…",
      "promptPreview":"…", "tokens":10552, "toolCalls":3,
      "startedAt":…, "queuedAt":…, "lastProgressAt":… } ] }
```
`workflow_progress[]` also contains `{ "type":"workflow_phase", "index", "title" }`
entries. Entry field names are camelCase in **both** the stream event and
the on-disk manifest, so one struct serves both.

### `task_notification` (terminal)
```json
{ "type":"system", "subtype":"task_notification", "task_id":"w8a0hi7jg",
  "status":"completed",                       // or "stopped" / "killed"
  "output_file":"/…/tasks/<taskId>.output",   // empty when not completed
  "summary":"Dynamic workflow \"…\" completed",
  "usage":{ "total_tokens":8468, "tool_uses":0, "duration_ms":2706 } }
```

### Previously-unhandled system subtypes (were `UnknownEvent`)
```json
{ "type":"system", "subtype":"task_updated", "task_id":"whq0xk3f4",
  "patch":{ "status":"completed", "end_time":1782725441950 } }

{ "type":"system", "subtype":"thinking_tokens",
  "estimated_tokens":50, "estimated_tokens_delta":50 }
```
`thinking_tokens` is an estimated-token ticker that appears in ordinary
sessions too, not just workflows.

## On-disk run state (out-of-band monitoring)

Located under `~/.claude/projects/<mangled-cwd>/<workflow-session-id>/`.
**Verified to be written even with `--no-session-persistence`** — only the
main conversation transcript (`<session>.jsonl`) is suppressed by that
flag; the workflow runtime persists its own state regardless (it needs it
for resume).

| Path | Contents | Cadence |
| --- | --- | --- |
| `workflows/<runId>.json` | Full snapshot: `status`, `result`, `phases[]`, `workflowProgress[]` (per-agent state/tokens/toolCalls/resultPreview), `totalTokens`, `totalToolCalls`, `durationMs`, `script`, `scriptPath` | Checkpointed continuously (a killed run showed partial progress + `status:"killed"`) |
| `subagents/workflows/<runId>/journal.jsonl` | Append-only `{type:"started",agentId,key}` / `{type:"result",agentId,key,result}` per agent | One pair per agent, as they finish — tailable |
| `subagents/workflows/<runId>/agent-<id>.jsonl` + `.meta.json` | Full per-agent conversation transcript (`{"agentType":"workflow-subagent"}`) | per agent |
| `workflows/scripts/<name>-<runId>.js` | The generated workflow script | once |

**Path discovery is free**: the `async_launched` tool_result provides
absolute `scriptPath` and `transcriptDir`, so the manifest/journal paths
derive with no guessing:
- `manifest  = <dir-of-dir-of scriptPath>/<runId>.json`  (`…/workflows/<runId>.json`)
- `journal   = <transcriptDir>/journal.jsonl`

### Caveats
- **Undocumented internal layout** — these paths/filenames/JSON shapes are
  CLI implementation details, not a stable contract, and can drift across
  versions. Parse defensively and preserve raw bytes.
- **GC / lifetime unverified** — fine during and right after a run;
  "read it days later" is untested.
- The in-stream `task_progress.workflow_progress[]` already carries the
  same live data, so the filesystem is a **complement** (out-of-band
  monitoring from another goroutine/process, fire-and-forget launches,
  post-stream result retrieval), not a rescue.

## What this SDK does with the above

1. `TaskEvent` gains `WorkflowName`, `OutputFile`, `EndTime`,
   `WorkflowProgress []WorkflowProgressEntry`, and an `IsWorkflow()` helper.
2. `task_updated` folds into `TaskEvent`; `thinking_tokens` becomes
   `ThinkingTokensEvent`. Both stop producing `UnknownEvent`.
3. `UserEvent` gains a parsed `WorkflowLaunch` (from the `async_launched`
   tool_result) with `ManifestPath()` / `JournalPath()` helpers.
4. `workflow.go` adds `ReadWorkflowSnapshot` (one-shot) and
   `WatchWorkflow` (polling the manifest, emitting typed snapshots
   out-of-band until a terminal status).

No new top-level event types, CLI flags, or triggering API are required.
```
