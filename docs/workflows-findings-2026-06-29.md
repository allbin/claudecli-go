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

---

## Addendum (2026-09-16): CLI 2.1.270

The original text above is unchanged. Some of it is wrong for current CLIs, and
this section corrects it. Probes ran `claude -p --output-format stream-json
--verbose --dangerously-skip-permissions` with a two-phase workflow: "Sleep"
ran two agents, each doing a Read and then a 90 s foreground
`python3 -c 'import time; time.sleep(90)'` (the CLI refuses a foreground
`sleep`), and "Join" ran one agent. A poller copied the transcript dir every
5 s. A third run used no phases and labels containing `": "`. The live
integration test `TestIntegrationWorkflowLiveState`
(`workflow_integration_test.go`) repeats the core checks through the SDK.
Trimmed captures are in `testdata/workflow/`.

### Corrections

- **The manifest is terminal-only.** The table above says `workflows/<runId>.json`
  is "checkpointed continuously". That was inferred from a killed run, and a
  kill is also a terminal write. In both polled runs no poll during the run
  found the manifest. Its mtime was 3 ms after the journal's last `result`
  line and about 1 s before the process exited. The SDK integration test
  checks the same thing on every run. `WatchWorkflow` is therefore deprecated
  and `ReadWorkflowSnapshot` is documented as reading the final state.
- **The stream's `workflow_progress[]` is not "the same live data" as a live
  manifest.** It arrives only on some ticks (below), and no live manifest
  exists.

### Live on-disk sources

Under `transcriptDir` (`<session>/subagents/workflows/<runId>/`), all written
while the run is live:

| File | Contents |
| --- | --- |
| `journal.jsonl` | `{"type":"launched"}`, then per agent `{"type":"started","key","agentId","label","phase"}` and `{"type":"result","key","agentId","result"}`, appended as agents start and finish. Without phases, `started` has no `phase` key. |
| `agent-<agentId>.jsonl` | The agent's transcript in Claude Code's session JSONL format: a `user` line with the prompt as a string, `attachment` lines (`deferred_tools_delta`, `environment`, `model`, `skill_listing`, `instructions`, `session_context`, `date`, `prompt_snapshot`, `auto_mode`, `total_tokens_reminder`), and `assistant` lines (one content block per line, with `message.model`) and `user` lines with `tool_result`. Every line has `uuid`, `timestamp`, `isSidechain: true`, `agentId`. A Bash `tool_use` is on disk while the command runs. |
| `agent-<agentId>.meta.json` | `{"agentType":"workflow-subagent","description":<label>,"workflowPhase","spawnDepth":1,"requestShape":"foreground","requestNonInteractive":true}`. Without phases there is no `workflowPhase` key. No `model` or `worktreePath` appeared in these runs. |

Agent ids seen: 17 lowercase hex characters (`acf991b72ff3c46ab`).

### `task_progress`: two kinds

- **Tree ticks** carry `workflow_progress[]`: phases plus agent entries with
  `state`, `lastToolName`, `toolCalls`, `tokens`, and a new field
  `promptFramed`. A queued agent has no `agentId` yet. They fire when an agent
  changes state or the phase changes.
- **Usage ticks** have `workflow_progress: null`. `usage.total_tokens` and
  `usage.tool_uses` are workflow-wide sums, and they never decreased.
- On **both** kinds, `description` names the agent that ticked:
  `"<phaseTitle>: <label>"`, or just `"<label>"` for a workflow without phases.
  `last_tool_name` is the label, not a tool. Splitting on `": "` is unsafe, as
  the phase-less run with labels `"check: one"` showed. The SDK matches the
  description against the last tree instead.
- Observed sequences: `TTnnnnTTnT` and `TTnnnnTTT` (two-phase runs). The workflow's
  `task_notification` carries no tree.

### Bash inside workflow agents

Each Bash call inside a workflow agent reaches the parent stream as a
`task_started` with `task_type:"local_bash"`, `"owned_by_subagent": true` and
`"is_backgrounded": false`, followed by a `task_notification`. **Unlike the
2026-09 brief, the `task_notification` does not carry `owned_by_subagent`**
(0 of 6 notifications across three runs). The SDK backfills it from
`task_started`. The task's `tool_use_id` equals the Bash `tool_use` id in the
owning agent's transcript, which links the stream task to the agent.

### Not forwarded

No stream event had `parent_tool_use_id` set, and none of the agents'
`tool_use` ids appeared on the stream. The raw probes did not pass the SDK's
forward-subagent-text setting. The integration test runs with
`WithForwardSubagentText()` and checks the same thing.
