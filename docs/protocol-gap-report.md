# Claude Code stream-json + control protocol: gap report

**Date:** 2026-08-21
**Library version under review:** `github.com/allbin/claudecli-go` @ `ba95af4` (v0.2.0)
**CLI under review:** `claude` 2.1.235 (native ELF, `~/.local/share/claude/versions/2.1.235`)

Research pass. **Update (2026-08-21): Tier 1 and most of Tier 2 have since been
implemented** — see the `[Unreleased]` section of `CHANGELOG.md`. Section C
below marks each recommendation's status. The inventories in sections A and B
remain an accurate description of the protocol at CLI 2.1.235.

## Sources and how each claim was verified

| # | Source | Trust | What it gave us |
|---|--------|-------|-----------------|
| 1 | Live CLI 2.1.235, bidirectional `--input-format stream-json --output-format stream-json` | Highest — observed behavior | Every control subtype marked **[live]** below was actually sent to a running CLI and its real response captured |
| 2 | `@anthropic-ai/claude-agent-sdk@0.3.235` → `sdk.d.ts` (378 KB) + `sdk-tools.d.ts` (156 KB) | Very high — ships with the CLI's own build | Exact request/response field names, types, optionality, and the maintainers' own doc comments |
| 3 | CLI binary string table (`strings` over the 330 MB ELF) | High — the shipped artifact | Subtypes that exist in the binary but are absent from the exported SDK union |
| 4 | `~/git/t3code/apps/server/src/provider/Layers/ClaudeAdapter.ts` | Corroborating — third-party integration | Confirmed the subagent-model and context-usage techniques are used in production, not just theoretically available |
| 5 | `claude --help` (2.1.235) | High | Launch-flag surface |

**Version pinning matters here.** The npm package installed at `/usr/local/lib/node_modules/@anthropic-ai/claude-code` is **2.0.14** and its `sdk.d.ts` is the *deprecated* Claude Code SDK — it describes a much smaller protocol (8 control subtypes, 7 message types) and is **not** what CLI 2.1.235 speaks. Anything derived from that file would have been wrong. The correct companion is `@anthropic-ai/claude-agent-sdk@0.3.235`, whose patch number tracks the CLI's.

**Reproducing the live checks.** The probe scripts used are not committed (they were scratch). The pattern: spawn `claude --print --verbose --output-format stream-json --input-format stream-json`, write one JSON object per line to stdin, read one per line from stdout. A control request is `{"type":"control_request","request_id":"<yours>","request":{"subtype":"…",…}}`; the reply is `{"type":"control_response","response":{"subtype":"success"|"error","request_id":"…","response":{…}|"error":"…"}}`. `cmd/capture` covers the read-only half of this already.

---

## A. Control request inventory

The authoritative union is `SDKControlRequestInner` in `sdk.d.ts:4011` — 34 members. Three further subtypes exist in the binary and answer on the wire but are **not** in that union; they are listed separately.

Legend: ✅ exposed · ⚠️ partially exposed · ❌ not exposed · **[live]** verified against a running CLI

### A.1 Client → CLI

| Subtype | Lib | Verified | Request fields | Success response |
|---|---|---|---|---|
| `initialize` | ✅ | [live] | `hooks?`, `sdkMcpServers?`, `jsonSchema?`, `systemPrompt?`, `appendSystemPrompt?`, `planModeInstructions?`, `toolAliases?`, `excludeDynamicSections?`, `agents?`, `title?`, `skills?`, `promptSuggestions?`, `agentProgressSummaries?`, `forwardSubagentText?`, `supportedDialogKinds?` | `commands[]`, `agents[]`, `output_style`, `available_output_styles[]`, `models[]`, `account`, `fast_mode_state?`, `fast_mode_disabled_reason?` |
| `interrupt` | ⚠️ | [live] | `cancel_queued?: boolean` | `still_queued: string[]`, `cancelled?: string[]` |
| `set_permission_mode` | ✅ | [live] | `mode` | `{mode}` echoed |
| `set_model` | ✅ | [live] | `model?: string \| null` | `{}` |
| `set_max_thinking_tokens` | ❌ | [live] | `max_thinking_tokens?: number\|null`, `thinking_display?: 'summarized'\|'omitted'\|null` | `{}` |
| `rename_session` | ❌ | [live] | `title: string` | `{}` |
| `set_color` | ❌ | [live] | `color: string` | **Rejected in `-p`:** `Unsupported control request subtype: set_color`. REPL-bridge only. |
| `mcp_status` | ✅ | [live] | — | `{mcpServers: McpServerStatus[]}` |
| `get_context_usage` | ❌ | [live] | — | See A.4 — large structured breakdown |
| `get_settings` | ❌ | [live] | — | `{effective, sources, applied}` |
| `get_usage` | ❌ | [live] | — | `{session, subscription_type, rate_limits_available, rate_limits, behaviors}` |
| `get_session_cost` | ❌ | [live] | — | `{text: string}` (ANSI-stripped `/usage` text) |
| `list_models` | ❌ | [live] | — | `{models: ModelInfo[]}` |
| `get_binary_version` | ❌ | [live] | — | `{version, buildTime}` |
| `background_tasks` | ❌ | [live] | `tool_use_id?: string` | `{backgrounded: bool}` when targeted; `{}` when backgrounding all (Ctrl+B) |
| `apply_flag_settings` | ❌ | [live] | `settings: Record<string,unknown>` | `{}` — **mutates live settings, including permission rules** (see A.5) |
| `stop_task` | ✅ | [live] | `task_id: string` | `{}` |
| `rewind_files` | ✅ | — | `user_message_id: string`, `dry_run?: boolean` | rewind result |
| `register_repo_root` | ✅ | — | `directory`, `reload_claude_md?`, `reload_plugins?`, `reload_skills?` | `{root: string}` |
| `reload_plugins` | ❌ | [live] | — | `{commands[], agents[], plugins[], mcpServers[], error_count}` |
| `reload_skills` | ❌ | [live] | — | `{skills: SlashCommand[]}` |
| `mcp_reconnect` | ✅ | — | `serverName: string` | reconnect result |
| `mcp_toggle` | ✅ | — | `serverName`, `enabled` | toggle result |
| `mcp_set_servers` | ❌ | [live] | `servers: Record<string, McpServerConfig>` | `{added[], removed[], errors{}}` |
| `mcp_call` | ❌ | — | `tool`, `arguments?`, `expires_at?`, `timeout_ms?`, `input_files?[]`, `output_files?[]` | MCP `CallToolResult` content (+ `staging` when staged) |
| `file_suggestions` | ❌ | [live] | `query: string` | `{suggestions: […]}` |
| `read_file` | ❌ | [live] | `path`, `max_bytes?`, `encoding?: 'utf-8'\|'base64'` | `{contents, absPath, truncated?, encoding?}` |
| `seed_read_state` | ❌ | [live] | `path: string`, `mtime: number` | `{}` |
| `cancel_async_message` | ❌ | [live] | `message_uuid: string` | `{cancelled: boolean}` |

### A.2 CLI → client (the client must answer these)

| Subtype | Lib | Purpose |
|---|---|---|
| `can_use_tool` | ⚠️ | Tool permission prompt. Library handles it but drops several request fields and cannot send `updatedPermissions` or `interrupt` back — see A.5. |
| `hook_callback` | ❌ | `{callback_id, input: HookInput, tool_use_id?}`. Only arrives if the client registered `hooks` in `initialize`. Library has no hook support at all. |
| `mcp_message` | ❌ | `{server_name, message: JSONRPCMessage}` — carries JSON-RPC for an in-process ("SDK-hosted") MCP server. Bidirectional. |
| `elicitation` | ❌ | `{mcp_server_name, message, mode?, url?, elicitation_id?, requested_schema?, title?, display_name?, description?}` → `{action:'accept'\|'decline'\|'cancel', content?}` |
| `request_user_dialog` | ❌ | `{dialog_kind, payload, tool_use_id?}`. Fails **closed**: a kind not listed in `initialize.supportedDialogKinds` degrades to the no-dialog path rather than hanging. |
| `oauth_token_refresh`, `host_auth_token_refresh` | ❌ | `@internal`, binary strings only — **unverified**. Host supplies a fresh credential after a 401. Not relevant to Agentique. |

The library's inbound dispatch (`session.go:1189`) has exactly one case, `can_use_tool`; everything else returns `unsupported control request: <subtype>`. That is a safe default, but it means hooks, SDK-hosted MCP servers, elicitations and dialogs are all unreachable.

### A.3 Not in the exported union, but real — [live] verified

These answer successfully on a real `-p` session despite being absent from `SDKControlRequestInner`:

- **`set_cwd`** — field is **`path`**, not `directory` (the binary's prose says "directory"; the validator says `path must be a non-empty string`, and `{"subtype":"set_cwd","path":"…"}` returns `{"status":"ok","cwd":"…","changed":false,"transcript_relocated":true}`). Other arms per the binary: `needs_trust` (re-send with `trust_accepted:true` + `trusted_directory`) and `rejected` with `reason` ∈ `not_found|not_a_directory|blocked_by_rule|unsafe_path`.
- **`get_workspace_diff`** — `{diff:{stats, perFileStats[], hunks[], skippedLarge[], restricted[], source:{kind:'working-tree'|'branch'}}}`. Caps: 5 s git timeout, 50 files, 1 MB/file.
- **`get_plan`** — `{exists:false}` or `{exists:true, plan, path}`.

Binary strings also name `submit_feedback`, `message_rated`, `mcp_authenticate`, `mcp_oauth_callback_url` — **unverified**, and all `@internal`/host-specific. Not worth pursuing.

### A.4 Corrections to the assumed library inventory

The list in the brief was `ping, initialize, interrupt, set_permission_mode, set_model, rewind_files, mcp_reconnect, mcp_toggle, stop_task, mcp_status`. Two corrections:

1. **`register_repo_root` is missing from that list** but is implemented (`Session.RegisterRepoRoot`, commit `53f5180`). Actual count is 11.
2. **`ping` is not a protocol subtype.** It does not exist in the CLI. `Session.Ping` (`session.go:300`) sends `{"subtype":"ping"}` and deliberately treats the resulting `Unsupported control request subtype: ping` **error** as proof of liveness — the comment at `session.go:343` says so outright. It works, but it burns an error path on every health check and would break silently if the CLI ever added a real `ping`. `get_binary_version` is a genuine, cheap, side-effect-free round-trip that should replace it.

### A.5 Two other protocol frames the library ignores

- **`control_cancel_request`** — `{"type":"control_cancel_request","request_id":"…"}`. Either side may withdraw its own in-flight request. Not a control_request; there is no reply. The library never sends it (so an abandoned `can_use_tool` stays parked CLI-side) and never handles receiving one.
- **`keep_alive`** — `{"type":"keep_alive"}`, no payload, emitted periodically by the CLI during long control requests; receivers **must ignore it**. `parse.go` has no case for it, so it falls through to the `default:` branch and surfaces as an `UnknownEvent`. Not observed in the short probe runs (**unverified in practice**), but it is documented as emitted and the handling gap is real either way.
- **`update_environment_variables`** — a **top-level stdin frame**, *not* a control subtype. [live] Sending it as `{"type":"control_request","request":{"subtype":"update_environment_variables",…}}` returns `Unsupported control request subtype`. Correct form is `{"type":"update_environment_variables","variables":{…}}`. Keys are allowlisted (the binary refuses "non-allowlisted keys" and special-cases `CLAUDE_CODE_OAUTH_TOKEN`); values must all be strings.

---

## B. Inbound stream-json events

`SDKMessage` (`sdk.d.ts:4310`) has 39 members. The library handles 13 top-level types and 8 `system` subtypes.

### B.1 What the library already covers

`system/init`, `system/status`, `system/compact_boundary`, `system/task_started|task_progress|task_updated|task_notification`, `system/hook_started|hook_progress|hook_response`, `system/thinking_tokens`, `system/files_persisted`, `assistant`, `user`, `result`, `stream_event`, `control_request`, `error`, `rate_limit_event`, `prompt_suggestion`, `tool_progress`, `tool_use_summary`, `auth_status`.

### B.2 Unhandled — each lands as `UnknownEvent`

| Wire | Value to an orchestrator | Shape |
|---|---|---|
| `system/background_tasks_changed` | **High.** *Level* signal with REPLACE semantics: the complete live-background-task set on every membership change. The `task_started`/`task_notification` pair is an *edge* signal — a missed bookend wedges a stale "running" indicator forever. The docs explicitly say consumers who only need "is background work running" should use this instead of pairing edges. Per-process: emits nothing at startup, so reset to empty on CLI (re)start. | `tasks: {task_id, task_type, description}[]` |
| `system/permission_denied` | **High.** Auto-denials that never produce a `can_use_tool` prompt (auto-mode classifier, `dontAsk`, deny rules). Without a permission surface, `ask` decisions are terminal and also land here. Advisory — `result.permission_denials` is authoritative. | `tool_name, tool_use_id, agent_id?, decision_reason_type?, decision_reason?, message` |
| `system/session_state_changed` | **High.** `idle | running | requires_action` straight from the CLI — far more reliable than inferring from result/assistant traffic. | `state` |
| `conversation_reset` (top-level) | **High.** Emitted by `/clear`, plan-mode exit, fresh-session flows. Consumer must mount a fresh transcript under `new_conversation_id` and drop the cached title. Silently ignoring it corrupts transcript state. | `new_conversation_id: UUID` |
| `system/commands_changed` | Medium. Full slash-command list pushed after mid-session change. REPLACE semantics. | `commands` |
| `system/api_retry` | Medium. Retry banner data — lets a UI show "retrying 2/5" instead of appearing hung. | retry counters, `error_status` |
| `system/control_request_progress` | Medium. `started`/`api_retry` progress for a long-running client-originated request, correlated by `request_id`. | `request_id, status, attempt?, max_retries?, retry_delay_ms?, error_status?` |
| `system/model_refusal_fallback` / `model_refusal_no_fallback` | Medium. Pairs with `SDKAssistantMessage.supersedes` for message retraction. | `retracted_message_uuids` etc. |
| `system/worker_shutting_down` | Medium. Advance warning with a reason (`host_exit`, …) — currently indistinguishable from a crash. | `reason` |
| `system/notification`, `system/memory_recall`, `system/local_command_output`, `system/plugin_install`, `system/elicitation_complete`, `system/mirror_error`, `system/informational` | Low. Display-oriented. | — |

### B.3 Subagent model and effort — **confirmed, with an important caveat**

t3code's claim is correct. Verified end to end on a live run.

**Model — available, and it is the *resolved* id.** The correlation chain:

1. `system/task_started` carries both `task_id` **and** `tool_use_id` (the spawning Agent/Task tool_use block's id), plus `subagent_type`, `task_type`, and `workflow_name` (set only when `task_type == 'local_workflow'`).
2. Subagent `assistant` messages carry `parent_tool_use_id` equal to that `tool_use_id`.
3. `message.model` on those snapshots is the authoritative model.

Observed live:

```
task_started    task_id=a42ed3cc731261c2c
                tool_use_id=toolu_01JERPk9VitoHXcysvhY4t26
                subagent_type=Explore  task_type=local_agent

assistant       parent_tool_use_id=toolu_01JERPk9VitoHXcysvhY4t26
                message.model=claude-haiku-4-5-20251001
                subagent_type=Explore  task_description="Compute 2+2"
```

The Agent tool input said `model: "haiku"` (an alias — `AgentInput.model` is only `"sonnet"|"opus"|"haiku"|"fable"`, and is **absent entirely** when the subagent inherits the parent's model). The stream resolved it to `claude-haiku-4-5-20251001`. So the snapshot is strictly better than the launch-time value, exactly as t3code's comment at `ClaudeAdapter.ts:2876` states.

**The caveat — this is gated.** Without `--forward-subagent-text`, subagent assistant messages are **not emitted at all**. Negative control on an otherwise identical run:

| Run | subagent `assistant` messages observed |
|---|---|
| with `--forward-subagent-text` | 2 (both carrying `model`) |
| without | **0** |

`task_started` / `task_updated` / `task_notification` appear in both runs, but none of them carry a model field. So per-subagent model is *unavailable* unless the flag is on. The library already exposes it as `WithForwardSubagentText()` (`option.go:272`) — the gap is not the flag, it is that the parsed events throw the data away (see B.4).

Two useful extras also confirmed on the same messages: `subagent_type` and `task_description` are wrapper-level siblings on `SDKAssistantMessage`, so a subagent message is self-describing without a lookup table.

**Effort — not in the stream. Confirmed absent.** Exhaustive grep of `sdk.d.ts` for `effort` yields only: `AgentDefinition.effort` (input), `Options.effort` (input), hook input `effort` (hooks only), `ModelInfo.supportsEffort`/`supportedEffortLevels` (capability metadata), `Settings.effortLevel` (config), and `get_settings`'s `applied.effort` (the **main session's** resolved effort). No stream event carries a subagent's effort. t3code does not read it from the stream either — it seeds from the Agent tool input at launch (`ClaudeAdapter.ts:3202`) and never refines it, which is the same thing this library would have to do. **This is an upstream gap, not a library gap.**

### B.4 Where the library loses the data it already receives

Two places, both fixable without any new protocol traffic.

**Assistant wrapper fields.** `parse.go` flattens each assistant message into per-block events (`TextEvent`, `ThinkingEvent`, `ToolUseEvent`). Those carry `ParentToolUseID` but **not** `Model`, `SubagentType`, or `TaskDescription` — the wrapper fields are discarded during flattening. So even with `WithForwardSubagentText()` on, a consumer cannot see which model a subagent ran on. This is the single highest-value fix in the report.

**`system/init` capabilities.** [live] The init frame carries a `capabilities` array — the protocol's own feature-negotiation mechanism:

```json
"capabilities": ["interrupt_receipt_v1", "interrupt_cancel_queued_v1", "msg_lifecycle_v1"]
```

`InitEvent` (`event.go`) does not parse it. The typedefs reference these tokens as the correct gate for optional behavior — `interrupt_receipt_v1` for the `still_queued` receipt, `interrupt_cancel_queued_v1` for `interrupt.cancel_queued` — yet the library currently feature-sniffs on `CLIVersion` strings instead. Parsing the array is a three-line change that replaces version comparison with the negotiation the CLI is already offering, and it is the right foundation for every optional feature in Tier 1/2.

The full init frame also carries `terminal_slash_commands`, `memory_paths`, `messaging_socket_path`, `fast_mode_state`, `fast_mode_disabled_reason`, `analytics_disabled`, and `product_feedback_disabled`, none of which are parsed. Only `capabilities` is load-bearing; the rest are optional.

### B.5 Context usage — `get_context_usage` exists and is the right answer

Confirmed. The library currently infers context from `ResultEvent.ContextSnapshot` (last API call's stream events) and `ModelUsage`, both of which go stale after compaction. `get_context_usage` is an on-demand, post-compaction-accurate control request.

[live] response keys:

```
categories, totalTokens, maxTokens, rawMaxTokens, autocompactSource, percentage,
gridRows, model, memoryFiles, mcpTools, agents, slashCommands, skills,
autoCompactThreshold, isAutoCompactEnabled, messageBreakdown, apiUsage
```

`SDKControlGetContextUsageResponse` (`sdk.d.ts:3346`) documents all of these except **`autocompactSource`**, which the live CLI returns but the typedef omits — a real field, undocumented upstream. (`SDKContextUsage`, a *different* and more compact type, describes it as "which input decided the window": `settings | clientdata | experiment | model-default | unknown-model`.)

The fields that matter for an orchestrator are few: `totalTokens`, `maxTokens`, `rawMaxTokens`, `percentage`, `isAutoCompactEnabled`, `autoCompactThreshold`, `model`. `gridRows` is TUI rendering data and should not be modelled. t3code maps exactly `totalTokens` → active, `maxTokens` → window, `isAutoCompactEnabled` (`ClaudeAdapter.ts:575`) and prefers it over every result-derived estimate (`ClaudeAdapter.ts:2218`).

There is a second, passive path: `SDKAssistantMessage.context_usage?: SDKContextUsage`, attached to the synthetic assistant message that delivers a `/context` result. Only present when the user ran `/context`, so it is not a substitute — but it is free to parse if the assistant wrapper is being widened anyway (B.4).

### B.6 Permission updates mid-session — **yes, two ways**

The `PermissionUpdate` union (`sdk.d.ts`) is:

```ts
{type:'addRules'|'replaceRules'|'removeRules', rules: {toolName, ruleContent?}[],
 behavior: 'allow'|'deny'|'ask', destination: Destination}
| {type:'setMode', mode: PermissionMode, destination: Destination}
| {type:'addDirectories'|'removeDirectories', directories: string[], destination: Destination}

Destination = 'userSettings'|'projectSettings'|'localSettings'|'session'
```

**Path 1 — `apply_flag_settings`. [live] verified to mutate permission rules.** This is the finding the brief was reaching for:

```
get_settings  → effective.permissions = null
apply_flag_settings {settings:{permissions:{allow:["Bash(echo probe-marker:*)"],
                                            deny:["Bash(rm:*)"]}}}  → success
get_settings  → effective.permissions = {"allow":["Bash(echo probe-marker:*)"],
                                         "deny":["Bash(rm:*)"]}
```

It merges into the *flag settings* layer, which is session-scoped and not persisted to disk. Any `Settings` key works, not just permissions — including `ultracode`, which `sdk.d.ts:7305` names as the intended delivery mechanism. Note the flag layer showed as `sources.flagSettings = null` in the same response even after a successful write, so read back from `effective`, not `sources`.

**Path 2 — `can_use_tool` response `updatedPermissions`.** The "always allow" flow. The CLI supplies ready-made suggestions on the request; the client echoes back the ones the user accepted. Captured live from a real prompt:

```json
{"subtype":"can_use_tool","tool_name":"Write","display_name":"Write",
 "input":{"file_path":"/tmp/probe-target.txt","content":"hello\n"},
 "description":"/tmp/probe-target.txt",
 "permission_suggestions":[
   {"type":"setMode","mode":"acceptEdits","destination":"session"},
   {"type":"addDirectories","directories":["/tmp"],"destination":"session"}],
 "decision_reason":"Path is outside allowed working directories",
 "decision_reason_type":"workingDir",
 "tool_use_id":"toolu_01NbjBgwHU6dbKU6ZXYiuwxS"}
```

The SDK explicitly recommends echoing these back rather than re-deriving rules, because they may encode compound-bash logic and directory grants a consumer would get wrong.

The library's `PermissionResponse` (`control.go`) is `{Allow, UpdatedInput, DenyMessage}` and marshals only `behavior`/`updatedInput`/`message` (`session.go:1262`). It **cannot** send `updatedPermissions`, and **cannot** send `interrupt: true` on deny — the flag that stops the turn outright when the user says no without guidance. It also drops most of the request: `decision_reason`, `decision_reason_type`, `tool_use_id`, `agent_id`, `title`, `display_name`, `description`, `blocked_path`, `suppress_always_allow_rule`, `requires_user_interaction`, `matched_ask_rule`, `classifier_approvable`. `tool_use_id` and `agent_id` matter most — without them a host cannot attribute a prompt to the tool call or the subagent that raised it.

### B.7 Setting sources — already covered

`--setting-sources <user,project,local>` exists on the CLI and the library already exposes `WithSettingSources(...)` (`option.go:163`). Semantics worth documenting in the README: omitted = load all (CLI default); `[]` = load none (SDK isolation mode); **`project` must be included for CLAUDE.md files to load at all**. Related and *not* exposed: `Options.managedSettings` (restrictive policy tier, filtered through an allowlist) — low value for Agentique.

Separately, `--include-hook-events` and `--forward-subagent-text` are already exposed; the launch-flag surface is in good shape overall. Flags present in 2.1.235 and absent from `buildArgs()` that may be worth a look: `--brief`, `--chrome`, `--ide`, `--from-pr`, `--teleport`, `--worktree`/`--tmux`, `--file`, `--ax-screen-reader`, `--remote-control`. None are orchestrator-relevant; listed for completeness.

---

## C. Recommendations, ranked

Ranked by value to a session orchestrator (Agentique). Signatures follow existing repo conventions: `Session` methods for control requests, `Option` for launch flags, typed events for stream additions.

**Status legend:** ✅ implemented · ⬜ not implemented. Where the shipped
signature differs from the one proposed here, the shipped one is noted.

Shipped: all of Tier 1, plus Tier 2 items 8–11. Deliberately not shipped:
`get_usage` (12) and the remaining display-layer stream events (13), both of
which stayed low-value; and everything in Tier 3.

### Tier 1 — clear wins

**1. ✅ Surface the assistant wrapper fields (per-subagent model).** No new protocol traffic; the data already arrives and is discarded. Highest value per unit of work in this report.

```go
// On TextEvent, ThinkingEvent, and ToolUseEvent — alongside the existing ParentToolUseID:
Model           string // resolved API model id, e.g. "claude-haiku-4-5-20251001"
SubagentType    string // e.g. "Explore"; empty for the main thread
TaskDescription string // subagent task description
```

Correlate to the parent via the existing `ParentToolUseID` == `TaskEvent.ToolUseID`. Requires `WithForwardSubagentText()`; document that dependency prominently, because without it these fields are silently always empty — the single most likely way a consumer gets this wrong. Consider having `task_backfill.go` also stamp the resolved model onto later `TaskEvent`s keyed by `task_id`, mirroring the existing `task_type` backfill.

**2. ✅ `get_context_usage`.** Shipped as `QueryContextUsage() (*ContextUsage, error)`. Fixes the known post-compaction staleness. Model only the useful fields; skip `gridRows`.

```go
func (s *Session) QueryContextUsage() (*ContextUsage, error)

type ContextUsage struct {
    Model                string
    TotalTokens          int
    MaxTokens            int
    RawMaxTokens         int
    Percentage           float64
    IsAutoCompactEnabled bool
    AutoCompactThreshold int    // 0 when absent
    AutocompactSource    string // undocumented upstream; keep as string
    Categories           []ContextCategory // {Name, Tokens, IsDeferred}
    Raw                  json.RawMessage   // full payload for forward-compat
}
```

**3. ✅ `apply_flag_settings` + `get_settings`.** Shipped, plus a `SetPermissionRules` convenience wrapper. The mid-session permission-rule answer, plus session-scoped `ultracode`/`effortLevel`. Keep the settings payload opaque — `Settings` is a large, fast-moving struct and modelling it would be a maintenance sink.

```go
func (s *Session) ApplyFlagSettings(settings map[string]any) error
func (s *Session) QuerySettings() (*SettingsSnapshot, error)

type SettingsSnapshot struct {
    Effective json.RawMessage
    Sources   json.RawMessage
    Applied   json.RawMessage // runtime-resolved; carries applied.effort
}
```

**4. ✅ Complete the permission round-trip.** Shipped; the widened request is delivered through a new `WithCanUseToolRequest` callback rather than by changing `ToolPermissionFunc`, keeping existing callers compiling. Additive to `PermissionResponse`; existing callers keep compiling.

```go
type PermissionResponse struct {
    Allow        bool
    UpdatedInput json.RawMessage
    DenyMessage  string
    UpdatedPermissions []json.RawMessage // echo back accepted suggestions
    Interrupt          bool              // deny-and-stop
}
```

And widen `ToolPermissionRequest` with at minimum `ToolUseID`, `AgentID`, `DecisionReason`, `DecisionReasonType`, `Title`, `DisplayName`, `Description`. `ToolUseID` and `AgentID` are the ones that unblock attribution.

**5. ✅ Four unhandled stream events.** Small, self-contained parser additions.

```go
type BackgroundTasksChangedEvent struct { Tasks []BackgroundTask } // REPLACE semantics
type PermissionDeniedEvent struct {
    ToolName, ToolUseID, AgentID, DecisionReasonType, DecisionReason, Message string
}
type SessionStateChangedEvent struct { State string } // idle|running|requires_action
type ConversationResetEvent struct { NewConversationID string }
```

`background_tasks_changed` deserves a doc note that it is a *level* signal and must replace the consumer's set, not be paired with `task_started`/`task_notification` edges.

**6. ✅ Parse `capabilities` from `system/init`.** Three lines, and it is the prerequisite for doing every other optional feature correctly — gate on the token the CLI advertises rather than comparing version strings.

```go
// On InitEvent:
Capabilities []string // e.g. ["interrupt_receipt_v1","interrupt_cancel_queued_v1","msg_lifecycle_v1"]

func (e *InitEvent) HasCapability(name string) bool
```

**7. ✅ Replace the `ping` hack with `get_binary_version`.** Same liveness guarantee, a real success path, and it returns something useful.

```go
func (s *Session) QueryBinaryVersion() (version, buildTime string, err error)
```

Keep `Ping` as a thin wrapper for compatibility; note in the changelog that it no longer relies on an error response.

### Tier 2 — worth adding

**8. ✅ `interrupt` with `cancel_queued`.** Shipped as `InterruptWithQueued(bool) (*InterruptReceipt, error)`. One round-trip to halt a session including its queued commands — the semantics a UI Stop button actually wants. Currently `Interrupt()` leaves queued messages to run.

```go
func (s *Session) InterruptWithQueued(cancelQueued bool) (stillQueued, cancelled []string, err error)
```

Gated by the `interrupt_cancel_queued_v1` capability on `system/init`; older CLIs ignore the field. `Interrupt()` also currently discards the `still_queued` receipt it already receives.

**9. ✅ `background_tasks`.** Shipped as `BackgroundTask(toolUseID)`. Ctrl+B semantics — background a blocking subagent/Bash without killing it. Complements the existing `StopTask`, which is destructive.

```go
func (s *Session) BackgroundTasks(toolUseID string) (backgrounded bool, err error) // "" = all
```

**10. ✅ `control_cancel_request` + `keep_alive`.** Shipped, but without a public `CancelControlRequest`: request ids are internal, so cancels are sent automatically on timeout and inbound cancels abort the handler. Correctness, not features. Send a cancel when a `can_use_tool` callback is abandoned (otherwise the CLI parks the request until its deadline); ignore inbound `keep_alive` instead of emitting `UnknownEvent`.

```go
func (s *Session) CancelControlRequest(requestID string) error
```

**11. ✅ `set_max_thinking_tokens` / `rename_session` / `reload_skills` / `reload_plugins`.** Shipped. `mcp_set_servers` was dropped: it needs a modelled MCP server config to be worth more than raw JSON. Cheap one-liners over machinery that already exists. `rename_session` is genuinely useful to Agentique (session titles in the UI); the others are situational.

```go
func (s *Session) SetMaxThinkingTokens(tokens *int, display string) error
func (s *Session) RenameSession(title string) error
func (s *Session) SetMCPServers(servers map[string]any) (*MCPSetServersResult, error)
func (s *Session) ReloadSkills() ([]SlashCommand, error)
func (s *Session) ReloadPlugins() (*ReloadPluginsResult, error)
```

**12. ⬜ `get_usage`.** Rate-limit utilization with `resets_at` — real value for an orchestrator scheduling work against a Claude MAX plan (know when the 5-hour window resets rather than discovering it via failures). Marked *Experimental* upstream, so keep `Raw` alongside typed fields.

```go
func (s *Session) QueryUsage() (*UsageReport, error)
```

**13. ⬜ Remaining stream events.** `api_retry`, `control_request_progress`, `commands_changed`, `worker_shutting_down`, `model_refusal_fallback`. Mostly display-layer; add opportunistically.

### Tier 3 — not worth it now

- **`mcp_call`** — invoke an MCP tool with no model turn. Genuinely powerful, but the staged-file variant (`input_files`/`output_files`, CAS etags, typed `staging` errors) is a large surface aimed at Cowork's synced-file lane. If added, do the unstaged form only.
- **`read_file`, `file_suggestions`, `get_workspace_diff`, `get_plan`, `get_session_cost`, `seed_read_state`, `list_models`, `cancel_async_message`** — built for remote thin clients (`--remote`) and the sidebar viewer. Agentique has direct filesystem and git access; these are round-trips to get something it can read locally. `seed_read_state` is the one that could matter later, if the library ever manages its own context trimming.
- **`set_cwd`** — [live] works, but not in the exported union, and the field name in the binary's own prose (`directory`) contradicts the validator (`path`). Undocumented surface with a known documentation defect; wait for it to land in the SDK union.
- **`set_color`** — [live] rejected in `-p`. REPL-bridge only. Do not add.
- **`oauth_token_refresh`, `host_auth_token_refresh`, `submit_feedback`, `message_rated`** — `@internal`, host-specific, unverified.

### Needs upstream

- **Per-subagent effort.** Nothing in the stream carries it (B.3). The best any consumer can do is echo the Agent tool input, which is absent on inheritance. Worth an upstream request: add `effort` to `task_started` alongside `subagent_type`, or to the `SDKAssistantMessage` wrapper alongside `subagent_type`.
- **`autocompactSource` on `SDKControlGetContextUsageResponse`.** Returned live, missing from the typedef (B.5). Minor upstream typedef fix.
- **`set_cwd` / `get_workspace_diff` / `get_plan` in `SDKControlRequestInner`**, and the `set_cwd` `path` vs `directory` prose defect.

### Explicitly not gaps

`--forward-subagent-text`, `--setting-sources`, `--include-hook-events`, `--effort` (including `xhigh`), `--json-schema`, `--max-budget-usd`, `--autocompact`, `--betas` are all already exposed by `option.go`. The launch-flag surface is in better shape than the control-protocol surface — the gaps concentrate almost entirely in control requests and stream-event parsing.

### Hooks — the largest single absence

Hook support (`initialize.hooks` + answering inbound `hook_callback`) is the biggest missing subsystem and is already tracked as a known gap. It is a design task rather than a protocol gap, so it is out of scope here beyond noting that the protocol side is small: register `hookCallbackIds` per matcher in `initialize`, then answer `hook_callback` requests by mapping `callback_id` back to a Go func. The `HookInput` union has 9 event types and `SyncHookJSONOutput` carries the `permissionDecision` / `additionalContext` / `updatedInput` levers.
