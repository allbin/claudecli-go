# claudecli-go — deep refactor audit (2026-05-25)

Findings-only audit. No code changes. Pre-1.0 library; breaking changes
acceptable but should be staged through a deprecation procedure (see
F-API-1). Two production consumers: `formica` (board/api/internal) and
`agentkit/runtime`. Secondary consumers seen in passing:
`agentique/backend/internal/session`, `reviewbot` — both use a smaller
surface and are noted only where they widen impact.

Scope: every `.go` under `~/git/claudecli-go/` except `cmd/`,
`authtest/`, `tmp/`. Tests read for coverage context. Audit performed
by two parallel Explore agents (executor/lifecycle and
events/options/errors), then synthesized and **filtered for false
positives** — 6 agent findings were rejected after re-reading the code
(documented at the bottom).

Three pre-known follow-ups are **out of scope** and not re-covered:
- Approval pump bypass in `--input-format stream-json` for built-in tools.
- Broad event-shape drift on `claude` binary upgrade.
- `ResultEvent.ContextSnapshot` being optional / consumer fallback.

---

## Executive summary

22 findings across 6 axes. None block today; several would compound
during the first real "claude binary upgrade lands a new event" or
"deploy on Windows" incident. Phasing recommendation at the end.

**Top 5 by leverage** (each unlocks future safety without changing
behavior):

1. **F-API-1** Adopt a deprecation procedure. There is no `// Deprecated:`
   convention, no `CHANGELOG.md`, and no shim period — `feat!` commits
   like `d5e7a6f` removed `WithMaxThinkingTokens` outright, breaking
   consumers at upgrade time. One paragraph in `README.md` + a doc
   comment convention unlocks every other finding here.

2. **F-LC-5** Windows leaks orphan CLI + MCP processes on parent
   crash. `executor_windows.go:11-14` is a no-op stub; `executor_unix.go`
   has `Setpgid` + `SIGTERM-to-pgrp` + 5s `WaitDelay`. Only painful for
   the consumer (agentkit/runtime) that runs on Windows hosts, but it
   leaks per crash.

3. **F-OPT-NAMING** Three high-impact naming/conflict footguns:
   `WithMaxBudget` (USD) vs `WithTaskBudget` (tokens); `WithEffort` vs
   `WithThinking` co-existing with implicit precedence; `WithSessionID`
   / `WithResume` / `WithContinue` mutual exclusivity enforced silently
   by `if/else-if/return` ordering. None panic — they silently pick a
   winner.

4. **F-EV-VERSIONING** No wire format version in the JSONL stream and
   `UnknownEvent` is silently dropped by consumers using the default
   type switch. Consumers can't tell "old SDK + new CLI" apart from
   "CLI hiccup." Even without breaking the wire, a single
   `WithUnknownEventHandler` callback would let consumers wire
   telemetry.

5. **F-TEST-INTEGRATION** The wire-shape test corpus
   (`testdata/*.jsonl`) is 4 small fixtures (basic, error, max_turns,
   tool_use) hand-written by maintainers — there is no captured
   real-CLI corpus for the 20+ event types in `event.go`. `cmd/capture`
   exists but is not wired into CI. A binary upgrade adding a new
   variant or renaming a field lands as a silent `nil` translation
   into consumers.

The remaining 17 findings are individually small but cluster around
**option surface hygiene**, **error-classification reach**, and **silent
parser fallbacks**.

---

## Recommended phasing

**Phase A — documentation only (zero risk, ~half-day):**
F-API-1, F-OPT-1, F-OPT-3, F-OPT-NAMING (note both budget options' units
in their doc comments), F-EV-5, F-EV-4, F-PARSE-3, F-LC-3 (document
the 250ms tradeoff). Lands as one commit, README + doc-comment churn
only.

**Phase B — small additive surface (no consumer breakage):**
F-EV-VERSIONING via `WithUnknownEventHandler`, F-LC-5 (Windows
`CREATE_NEW_PROCESS_GROUP` + `CTRL_BREAK_EVENT`), F-ERR-3 (extend
`ErrMaxTurns` to `RunBlocking`), F-PARSE-1 / F-PARSE-2 (emit
non-fatal `ErrorEvent` on the silent fallbacks instead of swallowing).
Each in its own commit. New options, new callbacks — no existing
consumer needs to change.

**Phase C — pre-1.0 cleanups requiring consumer coordination:**
F-OPT-NAMING (rename + deprecate old names), F-OPT-4 (clarify
`reservedFlags`), F-LC-11 (configurable scanner cap), F-TEST-INTEGRATION
(wire `cmd/capture` into CI as a golden-corpus regenerator).

**Out:** F-LC-1 / F-LC-9 / F-LC-10 / F-LC-13 are edge-case lifecycle
races. Each is a real bug, but each requires an unusual consumer
pattern to trigger. Leave for a "P3 lifecycle hardening" pass.

---

## Findings

### Executor + lifecycle

#### F-LC-1: `cfg.Stdin.Read()` blocking forever survives ctx cancel
**Severity:** P2
**Where:** `executor.go:88-105`
**What:** When `cfg.Stdin != nil`, two goroutines fire: one selects on
`ctx.Done()` to call `closeStdin()`, the other runs `io.Copy(stdinPipe,
cfg.Stdin)`. Closing `stdinPipe` (the write side) only unblocks
io.Copy's *write*. If `cfg.Stdin.Read` is itself blocked (e.g. a
consumer that never closes the reader on ctx cancel), the goroutine
leaks.
**Impact:** A consumer passing a custom `io.Reader` (rare — both
known consumers use the default stdin) that doesn't honor context can
leak one goroutine per session.
**Fix sketch:** Wrap `cfg.Stdin` in a ctx-aware reader, or document the
contract that `cfg.Stdin` must unblock on ctx cancel.
**Test coverage:** none — `integration_test.go` uses string stdin only.

#### F-LC-3: `CLIExitEvent` emit blocks Close path up to 250ms
**Severity:** P3 (design tradeoff, not a bug)
**Where:** `session.go:636-645`
**What:** Deferred emit at readLoop exit does a non-blocking send, then
falls back to a 250ms blocking wait. By design — the comment explains
the tradeoff between dropping the event and stalling `Close()`. Worth
documenting explicitly on `Session.Close` so consumers know `Close()`
can sleep ~250ms.
**Impact:** Closing many sessions in a tight loop (formica's
session-cleanup batch) pays up to 250ms × N when consumers have
stopped draining.
**Fix sketch:** Document on `Session.Close` and `CLIExitEvent`. No code
change.
**Test coverage:** `session_exit_test.go` covers the emit but not the
fallback-timer path.

#### F-LC-5: Windows leaks orphan CLI + MCP processes on parent kill
**Severity:** P1 (Windows only; agentkit/runtime is the host that
matters here)
**Where:** `executor_windows.go:11-14` (no-op) vs `executor_unix.go:14-20`
(Setpgid + SIGTERM-to-pgrp + 5s WaitDelay).
**What:** On Unix the CLI subprocess gets its own process group and a
TERM-on-cancel path; on Windows neither exists. If the parent panics
or is SIGKILLed before `Process.Wait` runs, the `claude` subprocess
and every MCP server it spawned stay alive, attached to nothing.
**Impact:** Per-crash leak of (claude + N MCP servers + tools) on
Windows. Compounds over a long-running host.
**Fix sketch:** `SysProcAttr.CreationFlags |= CREATE_NEW_PROCESS_GROUP`
in `setPlatformAttrs`; on cancel, send `CTRL_BREAK_EVENT` via
`GenerateConsoleCtrlEvent` to the pgrp; optionally attach via Windows
job object for descendant cleanup.
**Test coverage:** none for Windows; CI does not run a Windows target.

#### F-LC-9: Auth temp dir double-cleanup race
**Severity:** P3
**Where:** `auth.go:124-132` and `auth.go:196-208` — both `Wait` and
`Cancel` call `os.RemoveAll(p.tmpDir)`.
**What:** No `sync.Once`. Concurrent `Wait` + `Cancel` produces
"directory not empty" or "no such file" races. Real consumers don't
call both, but the documented contract permits it.
**Fix sketch:** `sync.Once` around the RemoveAll, or hand `tmpDir`
ownership to whichever runs first.
**Test coverage:** `auth_test.go` covers sequential cleanup; no
concurrent test.

#### F-LC-10: `Pool.forward` can drop events when session ctx cancels
mid-multiplex
**Severity:** P2
**Where:** `pool.go:184-201`
**What:** The forwarder reads from per-session events and writes to a
shared `p.events`. If the per-session ctx cancels (`Pool.Remove`)
while a write to `p.events` is parked, the select bails without
delivering. The event is lost; the consumer has no signal.
**Impact:** A consumer that removes a session while its `ResultEvent`
is in flight can miss it. Subtle source of "session ended but no
result observed" bugs.
**Fix sketch:** Either buffer the per-session event channel and drain
on cancel, or surface a "session removed mid-stream" sentinel on
`p.events`.
**Test coverage:** `pool_test.go::TestPoolSessionClose` uses fixtures;
no slow-consumer test.

#### F-LC-11: Scanner buffer cap is hard-coded at 10 MB; partial-line
hangs are silent
**Severity:** P2
**Where:** `session.go:685-686` — `scanner.Buffer(1MB, 10MB)`.
**What:** Two distinct issues: (a) a CLI JSONL line > 10 MB triggers
`bufio.ErrTooLong`, surfaced as a fatal `ErrorEvent` — but neither cap
is configurable; (b) `bufio.Scanner.Scan` blocks until newline or EOF,
so a CLI that writes partial JSON and stalls (MCP tool hangs mid-emit)
parks readLoop until `Ping` watchdog fires, with no
"partial-line-pending" signal.
**Impact:** Adaptive thinking traces and large tool results have been
trending up in size; consumer can't pre-emptively raise the cap.
**Fix sketch:** Surface `WithScannerMax(int)` option; surface
"awaiting full line" via the existing CLIStateChangeEvent / Ping path
(it's already partially there).
**Test coverage:** none for >10MB; none for stalled partial-line.

#### F-LC-13: Partial stdin write leaves session in inconsistent state
**Severity:** P3
**Where:** `session.go:487-502` `writeStdin`.
**What:** On a partial write (subprocess closing stdin mid-buffer), the
mutex-guarded write returns the IO error, but the half-sent control
message is now in the CLI's stdin buffer with no way to retract. The
caller times out via `controlTimeout`; the session is fine; but the
CLI may parse a truncated control request and emit a malformed
control_response that races the timeout.
**Impact:** Rare — needs the CLI to die between the first and last
byte of a >some-threshold control message. Manifests as a
"control error: parse" event tail before exit.
**Fix sketch:** Document; or on partial-write error, also mark stdin
as poisoned and refuse subsequent writes.
**Test coverage:** none.

---

### Events + parse

#### F-EV-VERSIONING: No wire format version + `UnknownEvent` silently dropped
**Severity:** P1
**Where:** `event.go:601` (UnknownEvent), `parse.go:56-88` (variant
dispatch), default case of consumer type switches.
**What:** Two compounding gaps: (a) the JSONL stream carries no
`schema_version` — consumers can't distinguish "old SDK + new CLI"
from "CLI bug"; (b) consumers writing the canonical `switch ev :=
event.(type)` happily ignore `*UnknownEvent` in the default case. New
event types from CLI upgrades vanish without trace.
**Impact:** When the CLI ships a new event variant (recent history:
`HookEvent`, `TaskEvent`, `TurnEvent`, `CompactStatusEvent`,
`CompactBoundaryEvent`, `ToolProgressEvent`, `CLIStateChangeEvent`,
`CLIExitEvent` all added since the SDK started), older SDK consumers
either miss it (`UnknownEvent`) or get a `nil` translation
(structurally-similar events). The user pre-flagged the broad issue;
this finding narrows it to *one shippable lever*.
**Fix sketch:** Add `WithUnknownEventHandler(func(*UnknownEvent))`
option, default no-op. Log to stderr in debug mode. Optional: emit a
client-side `SDKVersionMismatchEvent` on first unknown.
**Test coverage:** `parse_test.go::TestParseUnknownEventType` exists
but only validates the type is *created*, not surfaced.

#### F-EV-3: `TaskEvent` / `HookEvent` carry per-subtype field unions
**Severity:** P2
**Where:** `event.go:88-118` (TaskEvent), `event.go:129-155` (HookEvent).
**What:** Both event types have ~10 structured fields populated
*conditionally* by subtype. `task_started` populates Description /
TaskType / Prompt; `task_progress` populates LastToolName /
TotalTokens / ToolUses / DurationMs. Same shape collision in
HookEvent. The Raw json.RawMessage holds the full payload for
unrecognized subtypes — but consumers reading the typed fields can
silently see zero values for the wrong subtype.
**Impact:** formica's task tracker can read `TaskEvent.Prompt` on a
`task_notification` and get `""` instead of a parse error.
**Fix sketch:** Pre-1.0 break: split into `TaskStartedEvent`,
`TaskProgressEvent`, `TaskNotificationEvent`, `HookStartedEvent`,
`HookResponseEvent` (sealed). Each variant has only the fields it
populates. The current shape is convenient for parsing but pushes
the validation cost onto every consumer.
**Test coverage:** `parse_test.go::TestParseTaskEvents` covers each
subtype individually; no cross-subtype mixup test.

#### F-EV-4: `CLIStateChangeEvent` is emitted *before* its triggering event
**Severity:** P3 (by-design, but easy to misread)
**Where:** `session.go:664-677` (`pumpSend`).
**What:** When activity transitions on event `ev`, the pump emits the
transition first, then `ev`. Document comment lines 659-663 calls
this out, but it conflicts with the natural reading of "the state
change was caused by what just happened."
**Impact:** A consumer that reacts to `ActivityAwaitingToolResult` by
expecting the `ToolUseEvent` to already be in some side channel will
race.
**Fix sketch:** Add a paragraph to `CLIStateChangeEvent`'s godoc
explicitly: "Emitted *before* the event that triggered the
transition."
**Test coverage:** implicitly correct in fixtures, never asserted as
"ordering matters."

#### F-EV-5: `ToolProgressEvent` is only emitted by `Session`, not `ParseEvents`
**Severity:** P3 (doc gap)
**Where:** `event.go:536-554` (definition), `session.go:1036` (emit),
`parse.go` (no emit).
**What:** Synthetic event generated by the SDK's `Session` lifecycle
ticker, not from the CLI wire. Consumers using `Run` / `ParseEvents` /
the streaming `Stream` will never see it. Not stated in the godoc.
**Fix sketch:** One-sentence godoc: "Emitted by Session only; never
appears in `ParseEvents` output."
**Test coverage:** `session_test.go` covers Session emission; not
asserted absent from `parse_test.go`.

#### F-PARSE-1: `extractContent` silently wraps malformed JSON as text
**Severity:** P2
**Where:** `parse.go:223-265`.
**What:** Tries string → block array → raw-text fallback. The fallback
swallows malformed JSON: a content field of `{"type":"text"` (missing
brace) silently becomes `ToolContent{Type:"text", Text:"{\"type\":\"text\""}`.
**Impact:** Corrupted tool outputs flow through as text content with no
parse-error signal. The user sees garbled output.
**Fix sketch:** When both unmarshal attempts fail *and* the bytes look
like JSON (start with `{` or `[`), emit a non-fatal `ErrorEvent` and
still return the fallback for resilience.
**Test coverage:** `TestParseToolResultFallbackContent` covers the
happy fallback path; no malformed-JSON test.

#### F-PARSE-2: `updateContextSnapshot` silently ignores unmarshal errors
**Severity:** P2
**Where:** `parse.go:561-591`.
**What:** Each branch (`message_start`, `content_block_start`,
`message_delta`) does `if err := json.Unmarshal(...); err != nil {
return }`. A malformed `stream_event` payload leaves the snapshot
partially populated with no signal.
**Impact:** `ResultEvent.ContextSnapshot` can have InputTokens but
zero OutputTokens after a malformed delta; consumers think the model
emitted nothing. Token-cost reporting in formica becomes silently wrong.
**Fix sketch:** Emit a debug-level `ErrorEvent` on unmarshal failure;
keep partial data.
**Test coverage:** none for malformed payloads.

#### F-PARSE-3: `ParentToolUseID` null vs unset are indistinguishable
**Severity:** P3
**Where:** `parse.go:600-602`; `*string` in `rawEvent`, plain `string`
in exported event types.
**What:** Both `null` and absent become `""`. Currently no consumer
distinguishes them, but the conversion lossy-narrows the wire shape.
**Fix sketch:** Either commit to "null == absent == empty string" in
docs, or expose `*string` for callers who need to differentiate.

---

### Options

#### F-API-1: No deprecation procedure for breaking changes
**Severity:** P1 (process, not code)
**Where:** `README.md`, no `CHANGELOG.md`, no `// Deprecated:` convention.
**What:** `d5e7a6f feat!: remove WithMaxThinkingTokens and
SetMaxThinkingTokens` removed exported API outright. No alias
deprecation period, no compile-time warning, no changelog entry.
Consumers `git pull` and break. The library is pre-1.0 so this is
technically permitted, but it is the single biggest brake on
consumers upgrading promptly.
**Impact:** Both consumers (formica + agentkit/runtime) need to pin
versions and audit `git log --grep='feat!'` before bumps. Slows
adoption of unrelated improvements.
**Fix sketch:** One paragraph in README documenting the procedure:
(1) new commits use `// Deprecated:` on the old symbol, (2)
`feat!` commits land *after* at least one tagged release with the
deprecation visible, (3) `CHANGELOG.md` records every breaking
change with a one-line migration. Adopt
`golang.org/x/tools/internal/typeparams`-style aliases for the
common case.

#### F-OPT-1: `WithSessionID` / `WithResume` / `WithContinue` silent precedence
**Severity:** P2
**Where:** `option.go:358-384` `appendSessionArgs`.
**What:** Three options that emit mutually-exclusive CLI flags. The
function uses `if .. return; if .. return; if .. return; else
--no-session-persistence`. First-set wins by struct field
declaration order, not user-call order. No panic, no warning.
**Impact:** A consumer that programmatically composes Options and
ends up passing two will silently get one ignored. formica's
agentrunner has paths that build Options conditionally.
**Fix sketch:** In `resolveOptions`, panic if more than one of
`sessionID` / `resume` / `continueSession` is set; document the
exclusivity on each `WithX` godoc.
**Test coverage:** Each combination individually; no "both set"
negative test.

#### F-OPT-3: `WithEffort` + `WithThinking` co-exist with implicit precedence
**Severity:** P2
**Where:** `option.go:160-171`; `option.go:425-428`.
**What:** The godoc says they "overlap" but `buildArgs` happily emits
both `--effort` and `--thinking` flags if both are set. The CLI's
behavior under conflicting flags varies between versions (commit
`49a9181` flagged this for Opus 4.7). No precedence resolution in
the SDK.
**Impact:** Silent misconfiguration. The 4.7 migration commit
explicitly says "deprecate WithMaxThinkingTokens, add DefaultEffort"
but kept both options.
**Fix sketch:** When both are set, panic in `resolveOptions` (pre-1.0
allows this) OR pick one and log a deprecation warning. Document the
chosen winner.
**Test coverage:** Tested individually; no combined-set test.

#### F-OPT-4: `WithExtraArgs` reserved-flag set is incomplete
**Severity:** P3
**Where:** `option.go:180-189`, `option.go:515-520`.
**What:** `reservedFlags` includes `print`, `output-format`,
`input-format`, `verbose` — but not `model`, `max-turns`,
`session-id`, or any of the ~20 other flags the SDK always emits.
Future engineer adds a reserved flag and forgets to update the map.
**Fix sketch:** Generate the reserved set from the union of all
`appendXArgs` outputs at init time, or change the contract to
"WithExtraArgs is last-write-wins, not reserved."
**Test coverage:** Implicit test on the four listed flags; not a
property test.

#### F-OPT-NAMING: budget naming collision
**Severity:** P2
**Where:** `option.go:136` (`WithMaxBudget(usd float64)` →
`--max-budget-usd`) and `option.go:175` (`WithTaskBudget(totalTokens
int)` → `--task-budget`).
**What:** Visually-similar names; different units (USD vs tokens);
different scopes (session vs per-task). A consumer reaching for a
"limit total tokens" knob will probably try `WithMaxBudget` first.
**Fix sketch:** Pre-1.0 rename: `WithMaxSpendUSD(float64)` +
`WithMaxTaskTokens(int)`. Leave the old names as deprecated aliases
(per F-API-1).

#### F-OPT-WITHENV: `WithEnv` has no option_test.go coverage
**Severity:** P3
**Where:** `option.go:178` exports; `option_test.go` has no matching
test.
**What:** `buildEnv` (executor.go:153) handles overrides + the special
`CLAUDECODE` / `CLAUDE_CODE_ENTRYPOINT` / SDK version env vars, but
no test exercises the override path. A future change to env handling
has no regression net.
**Fix sketch:** Add a table-driven test asserting (a) overrides win
over inherited env, (b) `CLAUDECODE` is stripped from inherited env,
(c) `CLAUDE_AGENT_SDK_VERSION` is always present.

---

### Errors

#### F-ERR-3: `ErrMaxTurns` only surfaces in streaming mode
**Severity:** P2
**Where:** `error.go:23` defined; `parse.go:125-128` only emitted by
`ParseEvents`. `RunBlocking` (in `blocking.go`) takes the JSON
`--output-format json` path which doesn't go through `ParseEvents`.
**What:** A `RunBlocking` consumer hitting max turns will see a
generic `*Error{ExitCode:...}` and has to string-match `err.Error()`
or `Stderr` to detect it. `errors.Is(err, ErrMaxTurns)` returns
false.
**Impact:** Consumers using `RunBlocking` cannot use the typed-error
sentinel that streaming consumers can.
**Fix sketch:** In `blocking.go`'s error path, parse the JSON
`is_error: true, subtype: "error_max_turns"` and wrap into
`MaxTurnsError`. Mirror what `parse.go` does.
**Test coverage:** Streaming path tested (`TestParseMaxTurnsStream`);
blocking path not tested for max-turns.

#### F-ERR-INTERNAL: One `err.Error()` string-match in internal.go
**Severity:** P3
**Where:** `internal.go:126` reads `err.Error()` into a message string.
**What:** Not branching on the string, just using it for display.
Worth flagging because it's the only `err.Error()` usage outside tests
— a sanity check that the typed-error discipline elsewhere is intact.
**Fix sketch:** None needed; included for completeness.

---

### API stability

#### F-API-CONSUMER-SCAN: Surface used by zero or one consumer
**Severity:** P3 (cleanup)
**Where:** Cross-reference of `claudecli.*` exports vs grep across
formica + agentkit/runtime.
**What:** Used by neither consumer (candidates for hide / remove if
also unused by other internal consumers — verify in agentique-backend
and reviewbot first):
- `TurnEvent` — added in `9f2624e`; no consumer reads it.
- `HookEvent` — added in `a803e64`; no consumer reads it.
- `StreamEvent` / `ContextManagementEvent` — internal feeders for
  `ContextSnapshot`; no consumer type-asserts them. Could be
  unexported.

Used by exactly one consumer (formica):
- `CompactBoundaryEvent`, `CompactStatusEvent`, `RateLimitEvent`,
  `StderrEvent`, `TaskEvent`, `ErrorEvent`, `UserEvent`,
  `ToolResultEvent` — flag for "is this two-consumer-shaped or
  should formica-specific helpers move into formica?"

**Fix sketch:** Inventory pass. Don't remove yet — file an issue.

---

### Test coverage gaps

#### F-TEST-INTEGRATION: Wire-shape corpus is hand-written and tiny
**Severity:** P1
**Where:** `testdata/{basic,error,max_turns,tool_use}.jsonl` — 4 files,
all small, all hand-edited.
**What:** `event.go` defines 20+ event types; the fixture corpus
covers ~4 lifecycle shapes. `cmd/capture` was specifically built
(per `CLAUDE.md`) to record real CLI traffic but is not wired into
CI. A `claude` binary upgrade that adds a new event variant or
renames a field lands as a silent `nil` translation in consumers —
and this audit can't see whether that's already happened.
**Impact:** The single largest source of consumer-side surprise.
**Fix sketch:** (a) Regenerate testdata from `cmd/capture` against a
pinned `claude` binary version; (b) add a `make capture` recipe; (c)
add a CI smoke test that runs `cmd/capture -analyze` against a
golden file and asserts no `UnknownEvent`. The smoke can be opt-in
(skipped when binary unavailable) like `integration_test.go`.

#### F-TEST-RETRY: No rate-limit / overload retry test
**Severity:** P2
**Where:** No retry logic in the wrapper. `ErrRateLimit` /
`ErrOverloaded` / `RateLimitError.RetryAfter` are surfaced; consumer
is expected to retry.
**What:** This is intentional (the consumer owns retry policy), but
there's no test that proves the wrapper's classification is reliable
on a synthetic retry-after payload sequence.
**Fix sketch:** One table-driven test feeding rate-limit error events
through `ParseEvents` and asserting `errors.As(err, &*RateLimitError)`
plus `RetryAfter` correctness.

#### F-TEST-WINDOWS: Zero Windows test coverage
**Severity:** P2
**Where:** `executor_windows.go` (33 lines, never run).
**What:** CI runs Unix only. The Windows-side code path has zero
verified behavior, which compounds F-LC-5.
**Fix sketch:** Add a GitHub Actions windows-latest job that runs
`go test ./...` without `-race` and without integration. At minimum,
unit tests for `extractExitDetails` and the platform-specific cmd
builder.

---

## Summary table

| ID | Severity | Surface | One-liner |
| --- | --- | --- | --- |
| F-LC-1 | P2 | Executor | `cfg.Stdin.Read` blocking survives ctx cancel — goroutine leak |
| F-LC-3 | P3 | Session | `CLIExitEvent` emit blocks Close up to 250ms (by design — document) |
| F-LC-5 | P1 | Executor (Windows) | Orphan claude + MCP processes on parent kill — no process-group equivalent |
| F-LC-9 | P3 | Auth | Concurrent `Wait` + `Cancel` double-removes tmpDir |
| F-LC-10 | P2 | Pool | Forwarder can drop events when per-session ctx cancels mid-multiplex |
| F-LC-11 | P2 | Session | Scanner cap hard-coded at 10MB; partial-line stalls silent |
| F-LC-13 | P3 | Session | Partial stdin write on subprocess death — half-message lingers in CLI buffer |
| F-EV-VERSIONING | P1 | Events | No wire-format version; `UnknownEvent` silently dropped by consumers |
| F-EV-3 | P2 | Events | `TaskEvent` / `HookEvent` per-subtype field unions — silent zero values |
| F-EV-4 | P3 | Events | `CLIStateChangeEvent` emitted *before* triggering event (godoc only) |
| F-EV-5 | P3 | Events | `ToolProgressEvent` only emitted by Session, not ParseEvents (godoc only) |
| F-PARSE-1 | P2 | Parse | `extractContent` silently wraps malformed JSON as text |
| F-PARSE-2 | P2 | Parse | `updateContextSnapshot` silently ignores unmarshal errors |
| F-PARSE-3 | P3 | Parse | `ParentToolUseID` null vs unset indistinguishable |
| F-API-1 | P1 | API | No deprecation procedure for breaking changes |
| F-OPT-1 | P2 | Options | `WithSessionID`/`WithResume`/`WithContinue` silent precedence |
| F-OPT-3 | P2 | Options | `WithEffort` + `WithThinking` co-exist, emit both flags |
| F-OPT-4 | P3 | Options | `WithExtraArgs` reserved-flag set is incomplete |
| F-OPT-NAMING | P2 | Options | `WithMaxBudget` (USD) vs `WithTaskBudget` (tokens) collision |
| F-OPT-WITHENV | P3 | Options | `WithEnv` has no test coverage |
| F-ERR-3 | P2 | Errors | `ErrMaxTurns` only surfaces in streaming mode, not `RunBlocking` |
| F-ERR-INTERNAL | P3 | Errors | One `err.Error()` string read in internal.go (display only — sanity check) |
| F-API-CONSUMER-SCAN | P3 | API | Several exports used by zero or one consumer — hide/remove candidates |
| F-TEST-INTEGRATION | P1 | Test | No real-CLI wire-shape corpus; `cmd/capture` not wired into CI |
| F-TEST-RETRY | P2 | Test | No rate-limit / overload retry-classification test |
| F-TEST-WINDOWS | P2 | Test | Zero Windows test coverage |

---

## Event consumption matrix

Consumer surface from `grep -oE 'claudecli\.[A-Z][A-Za-z]+'` across each
consumer's `*.go`. "Yes" = explicit reference in non-test code; "test"
= test files only; "—" = absent.

| Event type | event.go | formica | agentkit/runtime | Notes |
| --- | --- | --- | --- | --- |
| StartEvent | :18 | Yes | — | Pre-process client-side event |
| InitEvent | :36 | Yes | Yes | Carries session ID |
| TextEvent | :180 | Yes | Yes | Core output |
| ThinkingEvent | :166 | Yes | — | Model reasoning (Opus 4.7) |
| ToolUseEvent | :210 | Yes | Yes | Tool invocation |
| ToolResultEvent | :264 | Yes | — | Tool output |
| ToolProgressEvent | :544 | Yes | Yes | **Session-only** (see F-EV-5) |
| UserEvent | :304 | Yes | — | Tool result + subagent completion |
| TurnEvent | :194 | — | — | **Unused by both** (F-API-CONSUMER-SCAN) |
| TaskEvent | :88 | Yes | — | Subagent lifecycle, multi-subtype (F-EV-3) |
| HookEvent | :129 | — | — | **Unused by both** (F-API-CONSUMER-SCAN) |
| CompactStatusEvent | :52 | Yes | — | Compaction in-progress |
| CompactBoundaryEvent | :66 | Yes | — | Compaction completed |
| RateLimitEvent | :360 | Yes | — | Rate limit status |
| StderrEvent | :379 | Yes | — | CLI stderr |
| ResultEvent | :389 | Yes | Yes | Carries usage and cost |
| ErrorEvent | :419 | Yes | — | Parse + CLI errors |
| CLIStateChangeEvent | :526 | Yes | Yes | Activity transitions |
| CLIExitEvent | :583 | Yes | Yes | Process termination reason |
| UnknownEvent | :601 | — | — | **Silently dropped** (F-EV-VERSIONING) |
| ControlRequestEvent | :433 | — | — | Internal — handled by SDK |
| StreamEvent | :445 | — | — | Internal feeder for ContextSnapshot |
| ContextManagementEvent | :492 | — | — | Internal |

---

## Option inventory (abridged)

Full table lives in the synthesis source — included here only the
options with audit findings or notable defaults.

| Option | Default | CLI flag | Notes |
| --- | --- | --- | --- |
| `WithModel` | `ModelSonnet` | `--model` | DefaultModel constant |
| `WithEffort` | (unset) | `--effort` | Conflicts with WithThinking (F-OPT-3) |
| `WithThinking` | (unset) | `--thinking` / `--max-thinking-tokens` | Conflicts with WithEffort (F-OPT-3) |
| `WithMaxBudget` | (unset) | `--max-budget-usd` | USD, session-wide (F-OPT-NAMING) |
| `WithTaskBudget` | (unset) | `--task-budget` | Tokens, per-task (F-OPT-NAMING) |
| `WithSessionID` | (unset) | `--session-id` | Wins over Resume/Continue (F-OPT-1) |
| `WithResume` | (unset) | `--resume` | Loses to SessionID (F-OPT-1) |
| `WithContinue` | (unset) | `--continue` | Loses to both above (F-OPT-1) |
| `WithForkSession` | false | `--fork-session` | Applies to whichever session mode is set |
| `WithExtraArgs` | (unset) | passthrough | Reserved set incomplete (F-OPT-4) |
| `WithEnv` | (unset) | (env) | No test coverage (F-OPT-WITHENV) |
| `WithStderrCallback` | (unset) | (callback) | Goroutine contract undocumented |
| `WithCanUseTool` | (unset) | (callback) | Session/Connect-only |
| `WithUserInput` | (unset) | (callback) | Session/Connect-only |

---

## Findings the audit rejected after re-reading the code

These were flagged by the Explore agents but did not survive validation.
Listed for transparency / future re-audits.

- **"`RateLimitError` and `MaxTurnsError` lack `Unwrap()` → break
  errors.Is/As chain."** Wrong: both implement `Is(target error)` which
  is sufficient for `errors.Is`. `errors.As` walks the outer error's
  Unwrap chain, not the inner — wrapped sentinels still work.
- **"`Error.Unwrap()` returns `[]error` not `error` → violates stdlib
  convention."** Wrong: `Unwrap() []error` is the Go 1.20+ multi-error
  signature; both signatures are valid.
- **"`handleControlResponse` can panic on double-send."** Wrong:
  `session.go:1106` uses `pending.LoadAndDelete`, so only one caller
  ever owns the channel.
- **"Tool-progress ticker leaks if ctx cancels between idempotency check
  and goroutine spawn."** Wrong: `toolProgressStop` is documented as
  "accessed only from the readLoop goroutine" (session.go:70), and
  both `startToolProgressTicker` and `stopToolProgressTicker` are
  called from readLoop. No concurrent access.
- **"Callback goroutine deadlocks if it panics with ctx already
  canceled."** Wrong: callback channel is buffered with cap 1; the
  recover branch writes into the buffer and returns, never blocking.
- **"Ping result channel send can panic if `failPendingRequests` closed
  it."** Wrong: `failPendingRequests` does a non-blocking *send*, not
  a close. Channels are never closed by either side; pending entries
  are deleted from the map. The Ping code (session.go:300-356) is
  defensive and correct, including the recent `df6ce28` fix.

---

## Verification

- `go test ./... -race -count=1` was not re-run as part of this audit
  (doc-only PR); CI should keep it green. The audit author should
  manually verify before merge.
- The same audit on `agentkit/runtime` would surface a *consumer-side*
  view of the same issues (e.g. how agentkit/runtime handles missing
  ContextSnapshot). Out of scope here; flagged for a follow-up.
