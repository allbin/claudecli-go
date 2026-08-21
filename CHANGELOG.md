# Changelog

All notable changes to `claudecli-go` are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the project aims to follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Install the latest release with:

```
go get github.com/allbin/claudecli-go@latest
```

or pin a specific version (e.g. `@v0.1.0`).

## [Unreleased]

### Fixed

- **Assistant events now carry the model, subagent type and task description.**
  The CLI reports these once per assistant message; the SDK flattened the
  message into per-block events and dropped them. `*TextEvent`, `*ThinkingEvent`
  and `*ToolUseEvent` gained `Model`, `SubagentType` and `TaskDescription`.

  This makes a subagent's model reachable for the first time. On a subagent
  message `Model` is the *resolved* API model id (e.g.
  `"claude-haiku-4-5-20251001"`), which is strictly better than the alias in the
  spawning Agent tool's input — `AgentInput.Model` is one of
  `sonnet|opus|haiku|fable` and is empty when the subagent inherits the parent's
  model. Correlate to the owning task by matching `ParentToolUseID` against
  `TaskEvent.ToolUseID` from `task_started`.

  Requires `WithForwardSubagentText()`. Without it the CLI emits no subagent
  assistant messages at all (verified against CLI 2.1.235: zero such messages
  without the flag), so the fields stay empty. There is no way to obtain a
  subagent's model without paying for the forwarded transcript.
- **`TaskEvent` now carries `SubagentType`** (e.g. `"Explore"`), sent by the CLI
  on `task_started` and `task_progress` and previously discarded.

### Added

- **`Session.BackgroundTask(toolUseID)`** — the `background_tasks` control
  request, i.e. Ctrl+B. Backgrounds a blocking subagent or Bash call so the
  turn continues while the work finishes, unlike the destructive `StopTask`.
  Pass an empty id to background every foreground task.
- **`Session.RenameSession(title)`** — sets the session's user-facing title.
- **`Session.SetMaxThinkingTokens(tokens *int, display ThinkingDisplay)`** —
  changes the extended-thinking budget and display mode mid-session. A nil
  budget resets to the session default. Cannot enable thinking on a session
  that has it disabled.
- **`Session.QueryBinaryVersion()`** — the CLI's own version and build time.
- **`Session.ReloadSkills()` and `Session.ReloadPlugins()`** — reload from disk
  and return the refreshed slash-command, agent and MCP-server surface.
- **`Session.InterruptWithQueued(cancelQueued bool) (*InterruptReceipt, error)`**
  — interrupts the running turn and returns what happened to queued work.

  `Interrupt()` discarded the receipt the CLI already sends, so callers could
  not tell that queued commands survived — and they do: the CLI starts the next
  one immediately after the turn aborts. `InterruptReceipt.StillQueued` lists
  the survivors; passing `cancelQueued` drops them instead and lists them under
  `Cancelled`, which is the one-round-trip "stop everything" a UI stop button
  wants. `Interrupt()` is unchanged and still omits `cancel_queued` entirely.

  Gated by the `interrupt_receipt_v1` / `interrupt_cancel_queued_v1`
  capabilities (see `InitEvent.HasCapability`); older CLIs answer with an empty
  body, which is handled as an empty receipt rather than an error.
- **`WithCanUseToolRequest(func(ToolPermissionRequest) (*PermissionResponse, error))`**
  — a tool-permission callback that receives the whole request instead of just
  the tool name and input. `ToolPermissionRequest` gained the fields the SDK
  was discarding: `ToolUseID`, `AgentID`, `DecisionReason`,
  `DecisionReasonType`, `ClassifierApprovable`, `Title`, `DisplayName`,
  `Description`, `BlockedPath`, `SuppressAlwaysAllowRule` and
  `RequiresUserInteraction`.

  `ToolUseID` and `AgentID` matter most: without them a host cannot attribute a
  permission prompt to the tool call or the subagent that raised it, which
  makes prompts ambiguous in any session running subagents in parallel.
  `WithCanUseTool` still works unchanged; the request-shaped callback takes
  precedence when both are registered.
- **`PermissionResponse.UpdatedPermissions` and `PermissionResponse.Interrupt`.**
  `UpdatedPermissions` completes the "always allow" flow — echo back the
  entries from `ToolPermissionRequest.PermissionSuggestions` the user accepted,
  which is safer than deriving rules from the tool input since a suggestion can
  encode compound-bash logic or a directory grant. `Interrupt` stops the turn
  outright on a denial, rather than returning the denial to the model; it is
  omitted from the wire unless set.
- **`Session.ApplyFlagSettings(map[string]any)`, `Session.SetPermissionRules(PermissionRules)`
  and `Session.QuerySettings()`** — the `apply_flag_settings` and
  `get_settings` control requests.

  **Permission rules can now be changed mid-session.** `SetPermissionMode` only
  switches the mode; `SetPermissionRules` replaces the allow/deny/ask rules
  themselves. Verified against CLI 2.1.235: `permissions` absent from the
  effective settings before the call, present after it. The flag layer is
  session-scoped — nothing is written to disk.

  `ApplyFlagSettings` accepts any key from the CLI's settings shape, which also
  makes `effortLevel` changeable mid-session and is the only way to set
  `ultracode` (session-scoped by design, with no CLI flag). `QuerySettings`
  returns the merged `Effective` view, the per-source `Sources` breakdown, and
  the runtime-resolved `Applied` view — the last being where the session's real
  effort level is reported. All three are raw JSON, since the CLI's settings
  shape is large and moves fast.
- **`Session.QueryContextUsage()`** — the `get_context_usage` control request,
  returning a live `*ContextUsage` breakdown of the context window.

  Prefer it over deriving context from a `ResultEvent` when the number must
  stay correct across compaction: `ResultEvent.ContextSnapshot` and
  `ModelUsage` describe the *last API call*, so neither shrinks when the CLI
  compacts and both drift upward until the next turn. This is measured on
  demand against the live transcript.

  Typed fields cover what an orchestrator needs — `TotalTokens`, `MaxTokens`,
  `RawMaxTokens` (the model's hard limit, which differs from `MaxTokens` when a
  smaller compaction-policy window applies), `Percentage`,
  `IsAutoCompactEnabled`, `AutoCompactThreshold`, `Categories`, and a
  `Remaining()` helper. The much larger remainder (per-tool and per-skill
  attribution, message breakdown, TUI grid data) stays available via `Raw`.
- **Four stream events that previously surfaced as `*UnknownEvent`:**
  - **`*ConversationResetEvent`** (`conversation_reset`) — emitted by `/clear`,
    plan-mode exit and fresh-session flows. A transcript boundary, not a
    session restart: the session id is unchanged but the model's context is
    gone. Consumers holding a transcript must start a fresh one under
    `NewConversationID`; ignoring it silently diverges from what the model
    sees.
  - **`*BackgroundTasksChangedEvent`** (`background_tasks_changed`) — the
    complete set of live background tasks after any membership change, with
    REPLACE semantics. This is a *level* signal; the
    `task_started`/`task_notification` pair is an *edge* signal, and a missed
    edge wedges a stale "running" indicator forever. Consumers that only need
    "is background work running" should read it here.
  - **`*SessionStateChangedEvent`** (`session_state_changed`) — the CLI's own
    `idle` / `running` / `requires_action`, with matching `SessionState*`
    constants. The only signal that distinguishes "waiting on the user" from
    "idle".
  - **`*PermissionDeniedEvent`** (`permission_denied`) — tool calls denied
    without an interactive prompt (auto-mode classifier, `dontAsk`, deny
    rules, headless auto-deny). Carries `ToolUseID` and `AgentID` for
    attribution. Advisory; `ResultEvent`'s permission denials remain
    authoritative.
- **`InitEvent.Capabilities` and `InitEvent.HasCapability(name)`.** The CLI
  advertises its optional protocol features on the init event (CLI 2.1.235
  sends `interrupt_receipt_v1`, `interrupt_cancel_queued_v1`,
  `msg_lifecycle_v1`); the SDK discarded the field. Gate optional behavior on
  `HasCapability` instead of comparing `CLIVersion` strings. Constants
  `CapabilityInterruptReceipt` and `CapabilityInterruptCancelQueued` are
  provided for the tokens this package acts on. Older CLIs omit the field
  entirely, so an empty slice means "nothing advertised", not "nothing
  supported".

### Fixed

- **`control_cancel_request` is now handled in both directions.** The frame
  withdraws an in-flight control request, and the SDK ignored it entirely.

  Inbound: when the CLI withdraws a permission prompt — its turn was
  interrupted, or another client answered — the in-flight callback is now
  cancelled and no reply is sent. Previously the callback ran to completion and
  wrote its answer to a dead `request_id`.

  Outbound: a control request that times out now sends a cancel, so the CLI can
  abort the work instead of holding it. A prompt the SDK stopped waiting on
  otherwise stayed parked until its own deadline.
- **`Session.Ping` no longer relies on an error response.** It sent a `"ping"`
  subtype, which the CLI has never implemented — liveness was proven by the
  resulting `Unsupported control request subtype` error. It now sends
  `get_binary_version`, a real side-effect-free request-response. Error
  responses are still accepted as proof of life, so CLIs predating that subtype
  keep working. Behavior and signature are unchanged.
- **`keep_alive` frames are no longer surfaced as `*UnknownEvent`.** The CLI
  emits this payload-less heartbeat periodically (for example while a long
  control request is in flight) and the protocol requires receivers to ignore
  it. It is now consumed silently instead of reading as a parse failure.

### Changed

- **Both decode loops now share one event decoder.** `ParseEvents` (for `-p`
  streams) and `Session.readLoop` (for `Connect()`) each carried their own copy
  of the same wire-format switch, so a case added to one and not the other
  worked in one mode and silently not the other. The stateless types now decode
  once in `decodeStatelessEvent`, and assistant content blocks decode once in
  `parseContentBlock`. No behavior change — the same events are emitted in the
  same order.

## [0.2.0] - 2026-08-11

Catches the SDK up to Claude Code CLI 2.1.227. Verified end-to-end against a
live CLI at that version, not just against fixtures.

### Fixed

- **`WithUser` no longer emits `--user`.** The CLI removed the flag, so any run
  configured with `WithUser` died immediately with
  `error: unknown option '--user'`. The option is now a deprecated no-op, kept
  only so existing callers still compile — remove your calls to it.
- **`ModelDisplayName` now recognizes the Fable tier.** `"claude-fable-5"`
  returned the raw ID instead of `"Fable 5"`, even though `ModelFable` has been
  a supported constant.
- **`Session` now populates the full `InitEvent`.** The session stdout pump
  built its own `InitEvent` and had drifted from the one-shot parser, silently
  dropping every field below. Both paths now share one constructor.
- **`Session` now emits `*ThinkingTokensEvent`.** The `thinking_tokens` system
  subtype surfaced as `*UnknownEvent` in sessions while parsing correctly in
  one-shot runs.

### Added

- **`WithIncludeHookEvents()`** — emits `--include-hook-events`. Without it the
  CLI reports no hook activity at all, which made the already-implemented
  `*HookEvent` unreachable in practice.
- **`WithForwardSubagentText()`** — emits `--forward-subagent-text` (CLI
  2.1.208+). Forwards subagent text and thinking as `*TextEvent`/
  `*ThinkingEvent` with `ParentToolUseID` set, including nested subagents at
  depth 2+ (CLI 2.1.221+).
- **`WithPromptSuggestions()`** and **`*PromptSuggestionEvent`** — a predicted
  next user prompt after each turn. Sessions only: the CLI emits it *after* the
  turn's result message, which a one-shot `Run` treats as the terminal event.
- **`InitEvent` gained `MCPServerErrors`** (`[]MCPServerError`, CLI 2.1.219+),
  listing `--mcp-config` entries skipped by validation. These never appear in
  `MCPServers`, so an empty `MCPServers` with a populated `MCPServerErrors`
  distinguishes a rejected config from servers that failed at runtime. Also
  added: `CLIVersion`, `CWD`, `PermissionMode`, `OutputStyle`, `SlashCommands`,
  and `Plugins` (`[]PluginInfo`).
- **`WithSafeMode()`** — emits `--safe-mode`, disabling all customizations
  (CLAUDE.md, skills, plugins, hooks, MCP servers, custom commands and agents,
  output styles, workflows) while leaving auth, model selection, built-in
  tools, and permissions working.
- **`WithAutoCompact(string)`** — emits `--autocompact`; `"auto"` or a token
  count between 100k and 1M.
- **`WithExcludeDynamicSystemPromptSections()`** — moves per-machine sections
  (cwd, env info, memory paths, git status) into the first user message,
  improving prompt-cache reuse across machines and users.
- **`WithPluginURLs(...string)`** — emits `--plugin-url` for zip-over-HTTPS
  plugin installs.
- **`PermissionManual`** — the `manual` permission mode. As of CLI 2.1.200 this
  is what the CLI's own "default" maps to.
- **`Session.RegisterRepoRoot(dir) (string, error)`** — the runtime equivalent
  of `/add-dir`, via the `register_repo_root` control request (CLI 2.1.224+).
  `WithAddDirs` only applies at startup, so reaching a directory discovered
  mid-run previously meant tearing the session down and losing its context.
  Returns the directory the CLI actually registered: a relative path resolves
  against the *CLI's* working directory, which differs from the Go process's
  when `WithWorkDir` is set, so the returned value is authoritative and
  `filepath.Abs` is not. Not idempotent — the directory must exist and must not
  already be registered. Fires the CLI's `DirectoryAdded` hook.

### Changed

- `--include-hook-events`, `--forward-subagent-text`, and
  `--prompt-suggestions` are emitted only on the stream-json paths (`Run`,
  `Connect`). The CLI rejects all three under `--output-format json`, so
  `RunBlocking` omits them rather than failing.
- `--prompt-suggestions` is passed as `--prompt-suggestions true`. Its value is
  optional, so a bare flag would swallow whatever followed it.
- Model constants are documented as deliberately-unversioned aliases: the CLI
  resolves each to the latest release of its tier, so `ModelOpus` picked up
  Opus 5 with no SDK change. Pin a full ID only when you need reproducibility.

### Upgrade notes

- **Remove your `WithUser` calls.** It still compiles but now does nothing.
  Before this release it made the CLI exit immediately, so no working code
  depends on its behavior.
- Everything else is additive — new options, fields, and events; existing type
  switches keep compiling. Note that `InitEvent.PermissionMode` may report
  `manual` (`PermissionManual`), which is what the CLI's own "default" maps to
  as of 2.1.200.

## [0.1.2] - 2026-07-02

### Changed

- **`CLAUDE_AGENT_SDK_VERSION` now reports this module's own version** instead of
  a hardcoded `"0.3.0"`. The value is read from the enclosing binary's module
  info (`runtime/debug.ReadBuildInfo`), so consumers that import a tagged release
  advertise their pinned version automatically. Local/dev builds fall back to the
  `SDKVersion` placeholder, whose value changed from `"0.3.0"` to `"0.0.0-dev"`.
  This is a telemetry/User-Agent attribution field only — the CLI does not gate
  behavior on it (verified against claude CLI 2.1.197: the var is used solely for
  the API User-Agent and analytics, and defaults to `"unknown"` when unset).
  Paired with the existing `CLAUDE_CODE_ENTRYPOINT=sdk-go`, traffic is now
  honestly attributed to this Go SDK at its real version rather than
  impersonating the upstream Agent SDK.

## [0.1.1] - 2026-07-02

### Fixed

- **`TaskEvent.IsWorkflow()` now stays correct across a task's whole lifecycle.**
  The CLI stamps `task_type` only on the `task_started` event; the later
  `task_progress`, `task_updated`, and `task_notification` events for the same
  `task_id` omit it (verified against claude CLI 2.1.197). Previously `TaskType`
  was empty on those, so `IsWorkflow()` returned `false` and `WorkflowName` was
  blank for every workflow event after launch — including the terminal
  `task_notification` carrying `status:"completed"`. The SDK now backfills
  `TaskType` and `WorkflowName` onto a task's subsequent events from its
  `task_started`, so consumers can gate on `IsWorkflow()` for progress and
  terminal events, not only the launch. `TaskEvent.Raw` is unchanged (only the
  typed convenience fields are populated). Ordinary `local_agent` subagents are
  unaffected — they get their own `task_type` backfilled and `IsWorkflow()`
  stays `false`.
- Interactive `Connect()` sessions now emit `task_updated` as a `*TaskEvent`.
  It was previously surfaced as an `*UnknownEvent` (the one-shot `Run()` /
  `ParseEvents` path already handled it).

## [0.1.0] - 2026-06-29

### Added

- **Dynamic workflow support** — [dynamic workflows](https://code.claude.com/docs/en/workflows)
  surface through the existing `task_*` event stream as a single synthetic task
  (`TaskType == "local_workflow"`). See the README ["Dynamic workflows"](README.md#dynamic-workflows)
  section and `docs/workflows-findings-2026-06-29.md`.
  - `TaskEvent`: new fields `WorkflowName`, `OutputFile`, `EndTime`,
    `WorkflowProgress []WorkflowProgressEntry`, and the `IsWorkflow()` helper.
  - `UserEvent.WorkflowLaunch` — parsed from the `async_launched` tool result.
    `WorkflowLaunch` carries `RunID`, `WorkflowName`, `ScriptPath`,
    `TranscriptDir` and the `ManifestPath()` / `JournalPath()` helpers.
  - Out-of-band monitoring: `WatchWorkflow` (polls the on-disk run manifest and
    streams `WorkflowSnapshot`s until a terminal status), `ReadWorkflowSnapshot`
    (one-shot), with `WithPollInterval`, `WorkflowSnapshot`,
    `WorkflowProgressEntry`, `WorkflowPhase`, and the `ErrNoManifestPath` sentinel.
  - `ThinkingTokensEvent` (system subtype `thinking_tokens`) — running estimate
    of thinking-token usage.
- `Usage.TotalTokens()` / `ModelUsage.TotalTokens()` and `Usage.String()`.
- `ModelFable` constant for Claude Fable 5.
- `ModelDisplayName` (and `InitEvent.ModelDisplayName()`) renders a model ID as
  e.g. `"Opus 4.8"`.
- Additional event types discovered from CLI stream comparison.

### Changed

- `task_updated` is now parsed into `TaskEvent` (previously `*UnknownEvent`).
- `thinking_tokens` is now parsed into `ThinkingTokensEvent` (previously
  `*UnknownEvent`).

### Fixed

- Windows: set `CREATE_NO_WINDOW` to suppress a console flash when the parent
  process has no console.
- Synthetic CLI api-error messages (`isApiErrorMessage`) are surfaced as a fatal
  `ErrorEvent` instead of leaking the transport error to the caller as model text.
- Aligned sentinel error formatting; improved `ToolUseEvent.String()`.

### Upgrade notes

These changes are backward-compatible — they add fields, types, and functions;
existing type switches keep compiling. Two things to know when adopting:

- **A dynamic workflow run emits two `ResultEvent`s.** The first reports that the
  workflow launched in the background; the second carries the real answer once it
  completes. Consume the **last** `ResultEvent`, not the first.
- **`task_updated` / `thinking_tokens` are no longer `*UnknownEvent`.** If you
  matched them via `UnknownEvent.Type == "system/task_updated"` (or
  `"system/thinking_tokens"`), switch to the new typed events:

  ```go
  case *claudecli.TaskEvent:            // e.Subtype == "task_updated"
  case *claudecli.ThinkingTokensEvent:  // e.EstimatedTokens / e.EstimatedTokensDelta
  ```

[Unreleased]: https://github.com/allbin/claudecli-go/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/allbin/claudecli-go/compare/v0.1.2...v0.2.0
[0.1.2]: https://github.com/allbin/claudecli-go/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/allbin/claudecli-go/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/allbin/claudecli-go/releases/tag/v0.1.0
