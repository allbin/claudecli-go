# Changelog

All notable changes to `claudecli-go` are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the project aims to follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

There are no tagged releases yet — everything below is unreleased. Pull the
latest with:

```
go get github.com/allbin/claudecli-go@latest
```

## [Unreleased]

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
