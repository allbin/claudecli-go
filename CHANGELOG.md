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

## [0.6.0] - 2026-08-24

### Added

- **`Update`** — runs the CLI's own updater, for the installs the CLI actually
  manages.

  `DetectInstall` could say *how* to update; nothing could do it. This closes
  that, and keeps the library's ownership of the `claude` command intact:
  consumers never construct a `claude` invocation themselves.

  ```go
  result, err := claudecli.Update(ctx, claudecli.WithUpdateProgress(func(line string) {
      fmt.Println(line) // "Checking for updates to latest version..."
  }))

  var manual *claudecli.ManualUpdateError
  switch {
  case errors.As(err, &manual):
      // Normal outcome, not a failure — the answer for most installs.
      fmt.Println("Update it yourself:", manual.Command)
  case errors.Is(err, claudecli.ErrUpdateNotWritable):
      // "Cannot" — never offer the button.
  case err != nil:
      // "Failed" — an error after the button was clicked.
  default:
      fmt.Println(result.VersionBefore, "->", result.VersionAfter, result.Changed)
  }
  ```

  Three things it does that a `exec.Command("claude", "update")` would not:

  - **Refuses installs the CLI does not manage.** Only `native` and `npm-local`
    are updated by `claude update`; npm-global, Homebrew, winget and version
    managers get an `ErrManualUpdate` carrying the command to display verbatim
    (`""` when none is known to be correct).
  - **Executes the detected PATH entry**, never the bare word `claude` (a second
    lookup can reach a different copy — two installs on one machine is common)
    and never the symlink-resolved binary (an `npm-local` entry is a `/bin/sh`
    wrapper; resolving past it drops the wrapper).
  - **Verifies by re-reading the version.** The exit code is not evidence: a
    sibling SDK measured its CLI's updater exiting `0` and printing "Update ran
    successfully" while the command it shells out to was not installed at all.
    `UpdateResult.Changed` compares the version either side of the run.

  A writability preflight on the directory the updater actually writes into
  (`<data>/claude/versions` for native, `~/.claude/local` for npm-local — not
  the directory holding the binary on PATH) returns the distinct
  `ErrUpdateNotWritable` before anything runs.

- **`LatestPublished`** — the published version for the channel *this* install
  tracks.

  `DetectInstall`'s doc used to say fetching this was the caller's business. It
  is better placed here, because only this library knows which channel a given
  install follows, and the channels disagree. Measured on one machine on one
  day: npm `latest` 2.1.241, native `latest` 2.1.241, native `stable` 2.1.231,
  Homebrew's `claude-code` cask 2.1.231. A consumer comparing against the wrong
  one manufactures a "behind" that is not true.

  ```go
  pub, err := claudecli.LatestPublished(ctx)
  if errors.Is(err, claudecli.ErrPublishedUnknown) {
      return // honest "cannot determine" — better than a wrong number
  }
  fmt.Println(pub.Version, pub.Channel, pub.Source, pub.UpdateAvailable)
  ```

  `ErrPublishedUnknown` means "no trustworthy source for *this* install", never
  "the lookup failed" — a failed request is an ordinary wrapped error, because
  that one is transient and worth retrying. Nothing degrades to a neighbouring
  channel to avoid returning it. And `UpdateAvailable` is only a verdict when
  `Comparable` is true: both versions parsed *and* the channel consulted is the
  one the install tracks. A blank verdict is correct; a wrong one is not.

  Sources resolve from the detected method: npm installs read the npm registry's
  dist-tags over plain HTTP (never `npm view` — a server has no npm on PATH),
  native reads the CLI's own release-channel endpoint, and Homebrew reads its
  cask (`claude-code` is stable, `claude-code@latest` is latest, whatever the
  settings say). Version managers, other OS package managers and unclassified
  installs return `ErrPublishedUnknown` rather than borrowing npm's number. The
  lookup is skipped entirely when `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` is
  set, matching the CLI's own updater.

  This one makes a network call and lives in its own file. `DetectInstall` stays
  offline and never calls it.

- **`InstallInfo.PathEntries`** — every copy of the CLI on PATH, in PATH order,
  with the one that runs marked `Active`. `info.Shadowed()` returns the rest.

  `ConfigMismatch` was the only second-copy signal, and it is a weak one: it
  needs the two copies to disagree about method *and* the config to record the
  loser. Measured on one ordinary machine, a native 2.1.241 in `~/.local/bin`
  winning over an npm-global 2.0.14 in `/usr/local/bin` — config said `native`,
  detection said `native`, they agreed, and `ConfigMismatch` stayed `false` while
  one PATH change would silently downgrade the running CLI by fifteen months.

  ```go
  for _, e := range info.Shadowed() {
      fmt.Printf("another copy is installed at %s (%s)\n", e.Path, e.Method)
  }
  ```

  Two copies is a warning, never a refusal: nothing fails or changes behaviour
  because of it, and `Update` still updates the winning copy. Each entry carries
  `Path`, `RealPath` and `Method`; no version is probed, because that is a
  process spawn per copy. Distinct copies are told apart by resolved path, so a
  duplicated PATH entry or two symlinked directories count once. The walk is
  `exec.LookPath`'s own work without the early exit — ~100µs on a two-copy
  machine, against the ~200ms `claude -v` probe detection already pays.

- **`InstallInfo.AutoUpdate`** — the CLI's own background-updater state, read
  from files `DetectInstall` already opens: whether auto-updates are on and what
  turned them off (`autoUpdates` in the config, `DISABLE_AUTOUPDATER`,
  `DISABLE_UPDATES`, or an OS package manager owning the install), which release
  channel it tracks, and the CLI's record of its last background attempt. A
  consumer that knows an install updates itself and last succeeded two days ago
  can say so instead of nagging about a version already being handled.

  No extra process and no network call — notably not `claude doctor`, which
  reports the same three facts by rewriting the config file to answer.

## [0.5.0] - 2026-08-23

### Added

- **`DetectInstall`** — read-only detection of how the `claude` CLI on PATH was
  installed, plus the command that updates *that* install.

  A host that wants to tell a user "you're on 2.1.87, 2.2.0 is published" also
  has to tell them how to update, and getting that wrong does not fail cleanly:
  `npm install -g` against a native install writes a second, complete copy into
  an npm prefix, and whichever copy PATH reaches first from then on is the one
  that answers `claude --version`. The version probe stops describing the binary
  that actually runs, and the copy the user reaches is still stale. Detecting
  this needs more than `exec.LookPath` plus a version parse — an npm global and
  an fnm shim look identical until the symlinks are resolved.

  ```go
  info, err := claudecli.DetectInstall(ctx)
  if errors.Is(err, claudecli.ErrCLINotFound) {
      return // normal state, not a failure
  }
  fmt.Println(info.Version, info.Method) // "2.1.87" "npm-global"
  if info.UpdateCmd != "" {
      fmt.Println("Update with:", info.UpdateCmd)
  }
  ```

  `InstallInfo` carries `Path`, `RealPath` (symlinks resolved), `Version`,
  `Method`, `UpdateCmd`, `VersionManager`, `PackageManager`, `PackageName`,
  `ConfigMethod`, `ConfigMismatch` and `Source`. `Method` is one of
  `InstallNPMGlobal`, `InstallNPMLocal`, `InstallVersionManager`,
  `InstallPackageManager`, `InstallNative`, `InstallUnknown`.

  `InstallUnknown` with an empty `UpdateCmd` is a legitimate answer and is
  always preferred to a guess — the caller shows "update manually" instead of a
  command that breaks the install.

  Precedence is package metadata → path layout → the CLI's config file. The
  `installMethod` recorded in the config says how the CLI was last *installed*,
  which need not describe the binary now first on PATH, so it only breaks ties;
  when it disagrees with conclusive path evidence the path wins and
  `ConfigMismatch` is set, flagging a shadowing second copy.

  Detection starts no session, writes nothing, and makes no network calls. It
  deliberately does not shell out to `claude doctor`, which reports the same
  facts but rewrites `.claude.json` and probes the network to do it. Fetching
  the *published* version stays the caller's business.

- **`ErrCLINotFound` / `CLINotFoundError`** — a missing CLI is a normal state
  for a consumer probing the environment, so `DetectInstall` reports it as a
  typed error the caller can tell apart from a real failure via
  `errors.Is(err, claudecli.ErrCLINotFound)`.

- **`CLIPackageName`** — the npm package that publishes the CLI
  (`@anthropic-ai/claude-code`), exported for consumers that query the
  published version themselves.

## [0.4.0] - 2026-08-21

### Added

- **`WithCanUseToolRequestContext` and `WithUserInputContext`** — permission
  callbacks that receive a per-request `context.Context`, so a host can learn
  that its prompt was withdrawn.

  v0.3.0 taught the SDK to handle inbound `control_cancel_request`: when the CLI
  withdraws a pending `can_use_tool` — its turn was interrupted, or another
  client answered — the handler is cancelled and no `control_response` is
  written. The callback itself was never told. Both registered shapes took no
  context, so a host that parks the decision on a human — the entire point of an
  interactive permission dialog — had no way to know the answer would be
  discarded. The prompt stayed on screen until the user answered something that
  went nowhere. Not a leak (session close still drains it), but a stale prompt
  with no way to detect staleness.

  The new `ctx` is cancelled when `control_cancel_request` arrives for that
  request id and when the session context ends. Both mean the same thing: the
  answer is discarded. A host should treat `ctx.Done()` as "drop the prompt".
  `WithUserInput` had the identical shape and the identical problem — a question
  put to a user always parks on one — so it got the same treatment in this
  release rather than being deferred.

  Purely additive: existing callbacks keep compiling and behaving identically.
  Precedence follows the existing rule that the more informed callback wins —
  `canUseToolReqCtx` > `canUseToolReq` > `canUseTool`, and `userInputCtx` >
  `userInput` — and is now documented on the options and on
  `ToolPermissionRequest`.

  Verified end-to-end against a live CLI 2.1.235, not just fixtures: a real
  `can_use_tool` prompt raised in `manual` permission mode, interrupted mid-park
  via `Session.Interrupt`, produced an inbound `control_cancel_request` carrying
  that request id, which cancelled the callback's ctx; the discarded `Allow`
  never reached the CLI (the target file was not written) and the session
  survived — the interrupted turn ended normally, `Ping` round-tripped, and a
  following query completed. See `TestIntegrationPermissionWithdrawalCancels`
  `CallbackCtx` (`-tags=integration`). The unit tests around it drive the
  cancel frame from a fixture.

## [0.3.0] - 2026-08-21

Catches the SDK up to Claude Code CLI 2.1.235, from a full survey of the
stream-json and control protocols at that version. Verified end-to-end against
a live CLI, not just against fixtures.

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

  **This is the only way to read the model's thinking text.** By default the
  CLI withholds it: `ThinkingEvent` arrives with a long `Signature` and empty
  `Content`, and `thinking_delta` stream events carry `thinking: ""` with only
  an `estimated_tokens` ping. Passing `ThinkingDisplaySummarized` turns on
  summarized reasoning. Verified against CLI 2.1.235; see
  [Reading thinking text](README.md#reading-thinking-text) for the two
  conditions (the display mode, and a model that emits thinking at all —
  Opus 5 does, Sonnet 5 did not).
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
- **Five stream events that previously surfaced as `*UnknownEvent`:**
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
  - **`*CommandsChangedEvent`** (`commands_changed`) — the full slash-command
    list after a mid-session change, with REPLACE semantics. Observed live
    whenever skills are discovered dynamically or `ReloadSkills` is called.
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

### Changed

- **Both decode loops now share one event decoder.** `ParseEvents` (for `-p`
  streams) and `Session.readLoop` (for `Connect()`) each carried their own copy
  of the same wire-format switch, so a case added to one and not the other
  worked in one mode and silently not the other. The stateless types now decode
  once in `decodeStatelessEvent`, and assistant content blocks decode once in
  `parseContentBlock`. No behavior change — the same events are emitted in the
  same order.

### Upgrade notes

Everything in this release is additive — new fields, events, options and
`Session` methods. Existing type switches and callbacks keep compiling. Two
behavior changes worth knowing about:

- **`keep_alive` no longer produces an `*UnknownEvent`.** If you were counting
  or logging unknown events, that stream gets quieter.
- **A withdrawn permission prompt is no longer answered.** When the CLI sends
  `control_cancel_request` for a pending `can_use_tool`, the callback is
  cancelled and no response is written. A callback that was relying on always
  reaching its "answer sent" path should treat cancellation as a normal
  outcome.

To get per-subagent model information you must opt in with
`WithForwardSubagentText()`. Without it the new `Model`, `SubagentType` and
`TaskDescription` fields on `*TextEvent` / `*ThinkingEvent` / `*ToolUseEvent`
are always empty — that is a CLI constraint, not an SDK one.

The full protocol survey behind this release, including what was deliberately
left out and what needs upstream changes, is in
[`docs/protocol-gap-report.md`](docs/protocol-gap-report.md).

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

[Unreleased]: https://github.com/allbin/claudecli-go/compare/v0.6.0...HEAD
[0.6.0]: https://github.com/allbin/claudecli-go/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/allbin/claudecli-go/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/allbin/claudecli-go/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/allbin/claudecli-go/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/allbin/claudecli-go/compare/v0.1.2...v0.2.0
[0.1.2]: https://github.com/allbin/claudecli-go/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/allbin/claudecli-go/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/allbin/claudecli-go/releases/tag/v0.1.0
