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

### Changed

- **Both decode loops now share one event decoder.** `ParseEvents` (for `-p`
  streams) and `Session.readLoop` (for `Connect()`) each carried their own copy
  of the same wire-format switch, so a case added to one and not the other
  worked in one mode and silently not the other. The stateless types now decode
  once in `decodeStatelessEvent`. No behavior change — the same events are
  emitted in the same order.

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
