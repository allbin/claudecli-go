# claudecli-go

Go package for invoking the [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code) as a subprocess with typed streaming events, functional options, and pluggable execution.

**Requires**: `claude` CLI installed and on PATH.

## Install

```
go get github.com/allbin/claudecli-go
```

See [CHANGELOG.md](CHANGELOG.md) for what changed between versions and upgrade notes.

## Quick start

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/allbin/claudecli-go"
)

func main() {
    // One-off blocking call
    text, result, err := claudecli.RunText(context.Background(), "Say hello in 5 words")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(text)
    fmt.Printf("Cost: $%.4f, Tokens: %d (%d in / %d out)\n",
        result.CostUSD, result.Usage.TotalTokens(),
        result.Usage.InputTokens, result.Usage.OutputTokens)
}
```

## Streaming

```go
stream := claudecli.Run(ctx, "Explain quicksort",
    claudecli.WithModel(claudecli.ModelSonnet),
)

for event := range stream.Events() {
    switch e := event.(type) {
    case *claudecli.TextEvent:
        fmt.Print(e.Content)
    case *claudecli.ThinkingEvent:
        // model thinking output
    case *claudecli.ToolUseEvent:
        fmt.Printf("[tool: %s]\n", e.Name)
    case *claudecli.StderrEvent:
        log.Println("stderr:", e.Content)
    case *claudecli.ErrorEvent:
        log.Println("error:", e.Err)
    case *claudecli.ResultEvent:
        fmt.Printf("\n--- Done: $%.4f ---\n", e.CostUSD)
    }
}
```

## Typed JSON responses

```go
type Analysis struct {
    Summary string   `json:"summary"`
    Tags    []string `json:"tags"`
}

analysis, result, err := claudecli.RunJSON[Analysis](ctx, client, prompt,
    claudecli.WithModel(claudecli.ModelHaiku),
)
```

`RunJSON` automatically strips markdown code fences (`` ```json ... ``` ``) before unmarshaling.

## Blocking mode

When you don't need streaming events, use `RunBlocking` for a simpler, more reliable path. Uses `--output-format json` internally.

```go
result, err := client.RunBlocking(ctx, "Summarize this file")
fmt.Println(result.Text)
fmt.Printf("Cost: $%.4f, Turns: %d\n", result.CostUSD, result.NumTurns)
```

For typed JSON with schema validation:

```go
type Analysis struct {
    Summary string   `json:"summary"`
    Tags    []string `json:"tags"`
}

// When WithJSONSchema is set, parses the schema-validated structured_output field.
// Otherwise, parses the text result with code fence stripping.
analysis, result, err := claudecli.RunBlockingJSON[Analysis](ctx, client, prompt,
    claudecli.WithJSONSchema(`{"type":"object","properties":{"summary":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}}},"required":["summary","tags"]}`),
)
```

## Cost & token usage

Every run reports its cost and token usage. `CostUSD` is the dollar cost; `Usage`
holds the token breakdown. Use `Usage.TotalTokens()` for the headline "tokens
used" figure (input + output + cache read + cache create) instead of summing the
fields by hand.

```go
text, result, err := claudecli.RunText(ctx, "Say hello")
// ...
fmt.Printf("cost $%.4f for %d tokens\n", result.CostUSD, result.Usage.TotalTokens())
fmt.Println(result.Usage) // Usage{in: 12, out: 5, cacheRead: 0, cacheCreate: 0, total: 17}
```

The same fields are on `BlockingResult` (from `RunBlocking`) and on each
`*ResultEvent` in streaming mode. The SDK reports cost and usage per call and
holds no running total — accumulating across runs is the caller's concern.

For a per-model breakdown (including context window and web search/fetch counts),
read `ResultEvent.ModelUsage`, keyed by model ID; each entry has its own
`CostUSD` and `TotalTokens()`.

## Client with defaults

```go
client := claudecli.New(
    claudecli.WithModel(claudecli.ModelSonnet),
    claudecli.WithPermissionMode(claudecli.PermissionPlan),
    claudecli.WithMaxBudget(0.50),
)

// All calls inherit these defaults
stream := client.Run(ctx, "Review this code")

// Per-call overrides replace defaults
stream := client.Run(ctx, "Quick check",
    claudecli.WithModel(claudecli.ModelHaiku),
)
```

## Authentication

Check, login, and logout via the CLI's `auth` subcommands.

```go
// Check current auth state (three-state: authenticated/unauthenticated/unknown)
status, err := client.AuthStatus(ctx)
fmt.Println(status.Status, status.Email, status.OrgName)

// Start OAuth login — returns URL for user to visit
proc, err := client.AuthLogin(ctx,
    claudecli.WithNoBrowser(), // required for SubmitCode
)
// Show AutoOpenURL to the user (localhost redirect).
// After authorizing, the browser redirects to localhost which fails on remote
// machines. User copies the failed redirect URL and passes it to SubmitCode.
fmt.Println("Visit:", proc.AutoOpenURL)

// User pastes the redirect URL (http://localhost:PORT/callback?code=X&state=Y):
err = proc.SubmitCode(redirectURL)

err = proc.Wait() // blocks until login completes

// Logout
err = client.AuthLogout(ctx)
```

Package-level shortcuts (`AuthStatus`, `AuthLogin`, `AuthLogout`) use the default client.
Use `NewClient([]ClientOption{WithLogger(logger)})` for debug logging.

| Login option              | Description                          |
| ------------------------- | ------------------------------------ |
| `WithAuthMethod(method)`  | `AuthMethodClaudeAI` (default) or `AuthMethodConsole` (API billing). |
| `WithSSO()`               | Force SSO login flow.                |
| `WithLoginEmail(string)`  | Pre-populate email on login page.    |
| `WithNoBrowser()`         | Suppress browser; required for `SubmitCode`. |

| LoginProcess field/method | Description                          |
| ------------------------- | ------------------------------------ |
| `URL`                     | Manual-visit URL (platform.claude.com redirect, shows CODE#STATE). |
| `AutoOpenURL`             | Browser URL (localhost redirect). Use this for remote/headless setups. |
| `CallbackPort() int`      | Port of the CLI's local callback server (0 if unavailable). |
| `SubmitCode(string) error`| Submit auth via localhost callback. Accepts a full redirect URL (`http://localhost:PORT/callback?code=X&state=Y`) or `CODE#STATE`. |
| `Wait() error`            | Block until login completes.         |
| `Cancel() error`          | Terminate the login process.         |

## Install method detection

`DetectInstall` reports how the `claude` binary on PATH was installed and which
command updates it — so a host that surfaces "you're on 2.1.87, 2.2.0 is
published" can show the *right* update command next to it.

```go
info, err := claudecli.DetectInstall(ctx)
if errors.Is(err, claudecli.ErrCLINotFound) {
    // Normal state, not a failure: no CLI installed.
    return
}

fmt.Println(info.Version, info.Method) // "2.1.87" "npm-global"
if info.UpdateCmd != "" {
    fmt.Println("Update with:", info.UpdateCmd)
} else {
    fmt.Println("Update manually — see https://code.claude.com/docs")
}
```

**Detect, never assume.** Suggesting the wrong update command does not fail
cleanly: `npm install -g` against a native install writes a second, complete
copy into an npm prefix, and whichever copy PATH reaches first from then on is
the one that answers `claude --version`. The user is told a version that does
not describe the binary their next session runs, and the copy they actually use
is still stale. Because that failure is silent, `InstallUnknown` with an empty
`UpdateCmd` is the correct answer whenever evidence is inconclusive.

Detection is read-only: it resolves the binary with `exec.LookPath` +
`filepath.EvalSymlinks`, reads package metadata and file headers next to the
resolved path, reads the CLI's config file, and runs `claude -v`. It starts no
session, writes nothing, and makes **no network calls**. Fetching the
*published* version (`npm view`, the releases API) is the caller's business.

> It deliberately does not shell out to `claude doctor`, which reports the same
> facts but rewrites `.claude.json` and probes the network to do it.

| `InstallInfo` field | Description |
| ------------------- | ----------- |
| `Path`              | Binary as found on PATH, before symlink resolution. |
| `RealPath`          | `Path` with all symlinks resolved — classification is driven by this. |
| `Version`           | What the CLI reports for itself, or `""` if the probe failed (not fatal). |
| `Method`            | `npm-global`, `npm-local`, `version-manager`, `package-manager`, `native`, `unknown`. |
| `UpdateCmd`         | Command to show the user; `""` when none is known to be correct. |
| `VersionManager`    | `fnm`/`nvm`/`volta`/`asdf`/`mise` when the binary sits under one, else `""`. Set even for `npm-global` — such an install only updates for the active node version. |
| `PackageManager`    | `homebrew`/`winget`/`mise`/`asdf` when `Method` is `package-manager`. |
| `PackageName`       | Homebrew cask name / winget package id. |
| `ConfigMethod`      | Raw `installMethod` from the CLI's config, verbatim (`native`/`global`/`local`). |
| `ConfigMismatch`    | Config disagrees with detected `Method` — usually a shadowing second copy. |
| `Source`            | `package-metadata`, `path-layout`, `config`, or `none`. |

**Precedence:** package metadata beats path layout, which beats the config file.
`installMethod` in the CLI's config records how the CLI was last *installed*,
which need not describe the binary now first on PATH, so it only breaks ties
(`Source` is then `config`). When it disagrees with conclusive path evidence,
the path wins and `ConfigMismatch` is set.

`client.DetectInstall(ctx)` uses that client's configured binary; the
package-level shortcut uses the default client.

## Stream state

Poll the stream's lifecycle state at any time:

```go
stream := client.Run(ctx, prompt)

// State is tracked automatically as events flow
stream.State() // StateStarting -> StateRunning -> StateDone/StateFailed

// Block until completion
result, err := stream.Wait() // idempotent, safe to call multiple times
```

States: `StateStarting`, `StateRunning`, `StateDone`, `StateFailed`.

Session lifecycle differs: `StateStarting` → `StateIdle` (after Connect) → `StateRunning` (during Query) → `StateIdle` (after result) → `StateDone` (after Close).

### Activity state (watchdogs)

Lifecycle state tells you *what phase the session is in*; activity state tells you *what the CLI is doing right now*. For watchdogs that time out on event silence, use activity state to distinguish "model is generating" from "CLI is executing a tool" from "between turns":

```go
for ev := range session.Events() {
    if cs, ok := ev.(*CLIStateChangeEvent); ok {
        // cs.State is one of:
        //   ActivityIdle              — between turns
        //   ActivityThinking          — model is generating
        //   ActivityAwaitingToolResult — a top-level tool_use is outstanding
        resetTimer(cs.State)
    }
}

info := session.ProcessInfo() // {LastStdoutAt, ActivityState, Lifecycle, SessionID}
```

`CLIStateChangeEvent` is emitted immediately BEFORE the event that triggered the transition (e.g. before the first `ToolUseEvent` of a turn), so consumers can update their state before processing the triggering event. `ProcessInfo().LastStdoutAt` is stamped from the stdout scanner independent of parsed events, so a stall can be distinguished from a quiet turn without inferring it from `ToolUseEvent`/`ToolResultEvent` pairing.

While the session sits in `ActivityAwaitingToolResult`, a `ToolProgressEvent` is emitted every 30 s carrying the first pending top-level tool_use's `ToolUseID`, `ToolName`, and `Elapsed` since it started. This is a pushed liveness signal for long tool runs — consumers can render `"Bash running for 4m 12s"` directly without polling `ProcessInfo()` or computing elapsed time themselves. Parallel tool_use calls do not change the reported tool; the first one is stable until the awaiting state ends.

## Sessions

```go
// Resume a previous session
stream := client.Run(ctx, "Continue where we left off",
    claudecli.WithSessionID("sess-abc123"),
)

// Fork from an existing session
stream := client.Run(ctx, "Try a different approach",
    claudecli.WithSessionID("sess-abc123"),
    claudecli.WithForkSession(),
)

// Continue the most recent session
stream := client.Run(ctx, "What were we doing?",
    claudecli.WithContinue(),
)
```

## Agents

```go
// Use a named agent
stream := client.Run(ctx, "Review this PR",
    claudecli.WithAgent("reviewer"),
)

// Define custom agents inline
stream := client.Run(ctx, "Check the code",
    claudecli.WithAgentDef(`{"reviewer": {"description": "Reviews code", "prompt": "You are a code reviewer"}}`),
    claudecli.WithAgent("reviewer"),
)
```

## Interactive sessions

`Connect()` starts a bidirectional session with the CLI's control protocol. Supports multi-turn conversations, programmatic tool permission callbacks, and mid-session model/permission changes.

```go
session, err := client.Connect(ctx,
    claudecli.WithModel(claudecli.ModelSonnet),
    claudecli.WithCanUseTool(func(name string, input json.RawMessage) (*claudecli.PermissionResponse, error) {
        if name == "Bash" {
            return &claudecli.PermissionResponse{Allow: false, DenyMessage: "no shell"}, nil
        }
        return &claudecli.PermissionResponse{Allow: true}, nil
    }),
)
if err != nil {
    log.Fatal(err)
}
defer session.Close()

// Send queries
session.Query("What files are in this directory?")

// Read events
for event := range session.Events() {
    switch e := event.(type) {
    case *claudecli.TextEvent:
        fmt.Print(e.Content)
    case *claudecli.ResultEvent:
        fmt.Printf("\nDone: $%.4f\n", e.CostUSD)
    }
}

// Or block until completion
result, err := session.Wait()
```

Session methods:
- `Query(prompt)` — send a text-only user message (sets up result tracking for `Wait()`)
- `QueryWithContent(prompt, blocks...)` — send a message with text and multimodal content blocks
- `SendMessage(prompt)` — send a message without result tracking (can be called mid-turn)
- `SendMessageWithContent(prompt, blocks...)` — multimodal variant of SendMessage
- `Events()` — event channel
- `Wait()` — block until result (idempotent)
- `Interrupt()` — send interrupt signal
- `InterruptWithQueued(cancelQueued)` — interrupt and get an `*InterruptReceipt` reporting which queued commands survived (`StillQueued`) or were dropped (`Cancelled`). Plain `Interrupt` leaves queued commands to run, so the CLI starts the next one immediately after the turn aborts; passing `cancelQueued` halts the session in one round-trip, which is what a UI stop button usually wants. Gated by the `interrupt_receipt_v1` / `interrupt_cancel_queued_v1` capabilities — see `InitEvent.HasCapability`
- `Ping(timeout)` — round-trip a side-effect-free control request (`get_binary_version`) to prove the CLI's read loop is alive (not just that the process is running). Watchdog-friendly: any CLI response, including an error, counts as success
- `SetPermissionMode(mode)` — change permissions mid-session
- `SetModel(model)` — change model mid-session
- `RegisterRepoRoot(dir)` — grant tool access to another directory mid-session (runtime `/add-dir`), returning the directory the CLI registered. Unlike `WithAddDirs`, which is start-time only, this avoids tearing down the session to reach a newly discovered directory. A relative path resolves against the **CLI's** working directory — not the Go process's when `WithWorkDir` is set — so use the returned value rather than `filepath.Abs`. Not idempotent: the directory must exist and must not already be registered. Requires CLI 2.1.224+; fires the `DirectoryAdded` hook.
- `GetServerInfo()` — raw JSON from the initialize handshake
- `RewindFiles(userMessageID)` — rewind files to a checkpoint
- `ReconnectMCPServer(name)` — reconnect a named MCP server (non-blocking)
- `ReconnectMCPServerWait(name, timeout)` — reconnect and block until connected (polls `mcp_status`; 0 timeout = 10s default)
- `ToggleMCPServer(name, enabled)` — enable/disable an MCP server
- `StopTask(taskID)` — stop a running task
- `BackgroundTask(toolUseID)` — background a running foreground task (Ctrl+B semantics) instead of killing it: the blocking tool call returns immediately and the work continues, emitting a `task_notification` when it settles. Empty id backgrounds every foreground task
- `QueryContextUsage()` — live `*ContextUsage` breakdown of the context window. Prefer this over `ResultEvent.ContextSnapshot`/`ModelUsage` when the number must survive compaction: those describe the last API call and drift upward until the next turn
- `QuerySettings()` — effective, per-source and runtime-resolved settings (`Applied` is where the session's real effort level is reported)
- `ApplyFlagSettings(map[string]any)` — merge settings into the session-scoped flag layer. Accepts any settings key, including `effortLevel` and `ultracode` (which has no CLI flag)
- `SetPermissionRules(PermissionRules)` — replace allow/deny/ask rules mid-session. `SetPermissionMode` only switches the *mode*; this changes the rules themselves
- `SetMaxThinkingTokens(tokens, display)` — change the extended-thinking budget and display mode mid-session. A nil budget resets to the session default; cannot enable thinking on a session that has it disabled
- `RenameSession(title)` — set the session's user-facing title
- `QueryBinaryVersion()` — the CLI's version and build time
- `ReloadSkills()` / `ReloadPlugins()` — reload from disk and return the refreshed slash-command, agent and MCP-server surface
- `GetMCPStatus()` — query MCP server status (fire-and-forget)
- `QueryMCPStatus()` — query MCP server status, returns `[]MCPServerStatus`
- `LastEventAt()` — arrival stamp of the most recent event, including synthetic ones (`ToolProgressEvent`, `CLIStateChangeEvent`, `StderrEvent`). A stronger hang signal for watchdogs than `ProcessInfo().LastStdoutAt`, which does not move during long tool executions
- `Close()` — terminate session

### Query-scoped events (QueryCtx)

`Events()` is session-scoped: events keep flowing into one shared channel whether anyone reads or not. A consumer that reads per-query (start query → read until result → stop reading) will find the *previous* query's buffered `ResultEvent` at the top of the channel on its next query — the classic off-by-one answer bug. `QueryCtx` eliminates this by correlating events to queries at arrival time:

```go
handle, err := session.QueryCtx(ctx, "What is Go?")
if err != nil { ... }

for event := range handle.Events() { // only THIS query's events
    switch e := event.(type) {
    case *claudecli.TextEvent:
        fmt.Print(e.Content)
    case *claudecli.ResultEvent:
        fmt.Printf("\nDone: $%.4f\n", e.CostUSD)
    }
}

// Or: result, err := handle.Wait(ctx)
```

Every event is stamped on arrival with the query generation that was active when it arrived (terminal events clear the generation). A dedicated router delivers events to the active query's bounded mailbox, and everything else — events before the first query, between queries, or after a handle was abandoned — to a bounded orphan mailbox. The router never blocks the stdout read loop: both mailboxes drop oldest first under overflow (see `RouterStats`).

- `QueryCtx(ctx, prompt)` / `QueryCtxWithContent(ctx, prompt, blocks...)` — like `Query`/`QueryWithContent` but returns a `*QueryHandle`. First call switches the session into routed mode
- `EnableRouting()` — switch to routed mode explicitly (idempotent; implied by first `QueryCtx`). After this, `Session.Events()` no longer delivers — consume via handles + `DrainOrphans` and do not mix with `Query()`/`Events()`
- `DrainOrphans() []OrphanEvent` — fetch and clear the orphan mailbox. Each entry carries `Event`, `ArrivedAt` and `ActiveQueryAtArrival` (0 = none). Call before each new query; a late `ResultEvent` from an interrupted query shows up here and should be delivered separately, not thrown away
- `RouterStats()` — drop counters for the bounded mailboxes
- `QueryHandle.Events()` — the query's private channel; closed on session end, `Detach()`, or when a newer `QueryCtx` supersedes it (buffered events remain drainable after close)
- `QueryHandle.Wait(ctx)` — consume until this query's terminal event (`ResultEvent`, fatal `ErrorEvent`, `CLIExitEvent`)
- `QueryHandle.Detach()` — stop delivery when abandoning a query (supersede/interrupt); later events for it land in the orphan mailbox tagged with `Gen()`
- `QueryHandle.Gen()` / `Dropped()` — generation for orphan correlation; drop counter for this mailbox

A `QueryCtx` whose stdin write fails marks the session `StateFailed` (stdin is poisoned — no events will ever arrive) so callers can recycle it instead of hanging in `StateRunning`.
### Rich tool permissions

`WithCanUseTool` receives only the tool name and input. `WithCanUseToolRequest`
receives the whole request, which is what a host needs to attribute a prompt
and to offer "always allow":

```go
session, err := client.Connect(ctx,
    claudecli.WithCanUseToolRequest(func(req claudecli.ToolPermissionRequest) (*claudecli.PermissionResponse, error) {
        // Attribute the prompt: which tool call, and which subagent raised it.
        fmt.Printf("%s (%s) wants %s: %s\n",
            req.ToolUseID, req.AgentID, req.ToolName, req.DecisionReason)

        switch req.DecisionReasonType {
        case "safetyCheck":
            return &claudecli.PermissionResponse{
                Allow:       false,
                DenyMessage: "safety checks are never auto-approved",
                Interrupt:   true, // stop the turn rather than telling the model to retry
            }, nil
        }

        // "Always allow": echo the CLI's own suggestions back rather than
        // deriving rules from the input — a suggestion can encode compound-bash
        // logic or a directory grant that is easy to get wrong.
        return &claudecli.PermissionResponse{
            Allow:              true,
            UpdatedPermissions: req.PermissionSuggestions,
        }, nil
    }),
)
```

`Interrupt` on a denial stops the turn outright; leave it false when
`DenyMessage` tells the model what to do instead.

Permission *rules* can also be changed independently of any prompt — see
`SetPermissionRules` above.

#### Withdrawn prompts

The CLI can withdraw a prompt before it is answered — its turn was interrupted,
or another client answered first. It sends `control_cancel_request`, the SDK
cancels the handler, and **no reply is sent for that request id**.

That matters most for the case the prompt exists for: a host that parks the
decision on a human. Without a signal, the dialog stays on screen waiting for an
answer that can no longer be delivered. `WithCanUseToolRequestContext` hands the
callback a per-request context so it can see the withdrawal:

```go
session, err := client.Connect(ctx,
    claudecli.WithCanUseToolRequestContext(func(ctx context.Context, req claudecli.ToolPermissionRequest) (*claudecli.PermissionResponse, error) {
        answer := ui.ShowPermissionDialog(req) // <-chan *claudecli.PermissionResponse

        select {
        case resp := <-answer:
            return resp, nil
        case <-ctx.Done():
            // Withdrawn, or the session ended. The answer would be discarded,
            // so take the dialog down instead of waiting on the user.
            ui.DismissPermissionDialog(req.ToolUseID)
            return nil, ctx.Err()
        }
    }),
)
```

The contract, in full:

- `ctx` is cancelled on an inbound `control_cancel_request` for this request id,
  and when the session context ends.
- Once cancelled, the return value is discarded — the SDK writes no
  `control_response`, and the CLI has stopped waiting for one. Returning an
  error there is not an error condition; nothing is sent either way.
- Treat `ctx.Done()` as *drop the prompt*. Returning promptly also releases the
  goroutine the SDK runs the callback in.
- A withdrawal is scoped to one request. The session stays live: the interrupted
  turn ends normally and later prompts reach the same callback.

Precedence follows the existing rule — the more informed callback wins:
`WithCanUseToolRequestContext` > `WithCanUseToolRequest` > `WithCanUseTool`. The
older shapes are unchanged; they simply cannot observe a withdrawal.

`WithUserInput` has the same shape and the same problem — a question put to a
user always parks on one — so `WithUserInputContext` carries the identical
contract, and outranks `WithUserInput`.

### Reading thinking text

By default the model's reasoning is withheld: `ThinkingEvent` arrives with a
long `Signature` and an empty `Content`, and the `thinking_delta` stream events
carry `thinking: ""` with only an `estimated_tokens` ping. That is the
redacted-thinking path — enough to drive a progress spinner (see
`*ThinkingTokensEvent`), not to read.

To get the text, ask for the summarized display mode:

```go
session, err := client.Connect(ctx,
    claudecli.WithModel(claudecli.ModelOpus), // must be a model that emits thinking
)
...
// The lever. A nil budget is fine — the budget is not what unlocks the text.
if err := session.SetMaxThinkingTokens(nil, claudecli.ThinkingDisplaySummarized); err != nil { ... }

session.Query("...")
for event := range session.Events() {
    if e, ok := event.(*claudecli.ThinkingEvent); ok && e.Content != "" {
        fmt.Println("reasoning:", e.Content)
    }
}
```

Two conditions must both hold, verified against CLI 2.1.235:

1. **`ThinkingDisplaySummarized` must be set** via `SetMaxThinkingTokens`. There
   is no CLI flag for it and no `Option` — only the `set_max_thinking_tokens`
   control request carries `thinking_display`, so it is reachable from
   `Connect()` sessions only. Setting a token budget alone changes nothing.
2. **The model must emit thinking.** Opus 5 does; Sonnet 5 produced no thinking
   blocks at all in the same test. The library's default model is `sonnet`, so
   an explicit `WithModel` is usually required.

What you get is *summarized* reasoning — a condensed paraphrase, not a
verbatim chain of thought. `ThinkingDisplayOmitted` suppresses it again.

`WithIncludePartialMessages()` is not required: the text arrives on the
assistant message's thinking block either way. Turn it on only if you want the
incremental deltas.

### Mid-turn message injection

`Query` rejects while a turn is running ("query already in progress") because it manages result tracking for `Wait()`. Use `SendMessage` to inject a message mid-turn — it writes directly to stdin without state gating:

```go
session.Query("Refactor the auth module")

// Later, while the agent is still working:
session.SendMessage("Also update the tests")
```

The CLI receives the message immediately but processes it at a safe boundary (between tool calls, not mid-generation). The injected message is folded into the current turn — the next `ResultEvent` from `Wait()` covers both the original query and injected messages.

`SendMessage` does not set up result tracking. If called without a prior `Query`, `Wait()` will hang. Use `Query` to start a turn, `SendMessage` to inject into it.

**Concurrency**: writes to stdin are mutex-serialized, so concurrent `SendMessage` calls are safe. Under extreme write volume the OS pipe buffer (64KB on Linux) provides natural backpressure — `SendMessage` blocks until the CLI drains stdin. If the pipe fills while the CLI is waiting for a control response (permission prompt), this could theoretically deadlock. In practice this requires dozens of queued messages and is unlikely for normal usage patterns.

**Delivery confirmation**: by default, `SendMessage` is fire-and-forget — you write to stdin with no acknowledgment. Enable `WithReplayUserMessages()` to have the CLI echo each user message back on stdout as a `UserEvent` with `IsReplay=true`. This confirms the CLI has read and accepted the message:

```go
session, err := client.Connect(ctx,
    claudecli.WithReplayUserMessages(),
)

session.Query("Start the refactor")
session.SendMessage("Also update the tests")

for event := range session.Events() {
    switch e := event.(type) {
    case *claudecli.UserEvent:
        if e.IsReplay {
            fmt.Printf("CLI confirmed: %s\n", e.Text())
        }
    }
}
```

### Forking an existing session

`WithForkSession` paired with `WithResume` / `WithContinue` / `WithSessionID` runs the CLI against a new session ID seeded with the parent's full history. The parent's session file on disk is not modified. Useful for asking a one-off question against a live session's context without polluting its transcript:

```go
result, err := client.RunBlocking(ctx, "Summarize this session in 2-4 words.",
    claudecli.WithResume(parentSessionID),
    claudecli.WithForkSession(),
    claudecli.WithModel(claudecli.ModelHaiku),
)
// result.SessionID is the fork's new ID (unique from parentSessionID).
// The parent session file on disk is byte-for-byte unchanged.
```

Prompt-cache hits on the shared prefix keep forks cheap. The fork writes its own session file to disk — callers that fork repeatedly should plan to clean these up. The parent must have been persisted (started via `Connect`, `WithSessionID`, `WithResume`, or `WithContinue`; `RunBlocking` without any of those defaults to `--no-session-persistence`).

### User input (AskUserQuestion)

When Claude calls the `AskUserQuestion` tool, it arrives as a `can_use_tool` control request. Use `WithUserInput` to handle these with a dedicated callback instead of routing them through `WithCanUseTool`:

```go
session, err := client.Connect(ctx,
    claudecli.WithUserInput(func(questions []claudecli.Question) (map[string]string, error) {
        answers := make(map[string]string)
        for _, q := range questions {
            // Present q.Header, q.Question, q.Options to your UI
            answers[q.Question] = getUserSelection(q)
        }
        return answers, nil
    }),
    claudecli.WithCanUseTool(func(name string, input json.RawMessage) (*claudecli.PermissionResponse, error) {
        return &claudecli.PermissionResponse{Allow: true}, nil
    }),
)
```

Routing rules:
- Both registered: `AskUserQuestion` → `userInput`, other tools → `canUseTool`
- Only `WithCanUseTool`: `AskUserQuestion` falls through to `canUseTool` (backward compatible)
- Only `WithUserInput`: `AskUserQuestion` → `userInput`, other tools get error response

Use `WithUserInputContext` instead when the question goes to a real user — it
adds the per-request context that fires when the CLI withdraws the question, so
you can take it off screen. Same contract as
[Withdrawn prompts](#withdrawn-prompts) above; it takes precedence over
`WithUserInput` when both are registered.

```go
claudecli.WithUserInputContext(func(ctx context.Context, questions []claudecli.Question) (map[string]string, error) {
    select {
    case answers := <-ui.Ask(questions):
        return answers, nil
    case <-ctx.Done():
        ui.DismissQuestions(questions)
        return nil, ctx.Err()
    }
})
```

## Multi-session pool

`Pool` is a registry that tracks multiple sessions and multiplexes their events into a single channel, tagged by session ID. The pool is purely additive — it doesn't modify Session or Client APIs.

```go
pool := claudecli.NewPool()
defer pool.Close()

s1, _ := client.Connect(ctx)
s1.Query("start task A")
// Wait for InitEvent so SessionID is set...

s2, _ := client.Connect(ctx)
s2.Query("start task B")

pool.Add(s1, claudecli.SessionMeta{Name: "task-a", Labels: map[string]string{"role": "worker"}})
pool.Add(s2, claudecli.SessionMeta{Name: "task-b"})

// Single event loop for all sessions
for pe := range pool.Events() {
    fmt.Printf("[%s] %T\n", pe.SessionID, pe.Event)
}
```

Pool methods: `Add`, `Remove`, `Get`, `List`, `Events`, `Close`, `CloseAll`. All are thread-safe. `CloseAll` closes every registered session in parallel (respecting each session's grace period), then closes the pool — useful for clean application shutdown.

### Inter-agent messaging

`FormatAgentMessage` wraps content in a structured format that Claude recognizes as peer communication. `Pool.SendAgentMessage` is a convenience that looks up sessions and calls `SendMessage` on the target.

```go
// Direct formatting
msg := claudecli.FormatAgentMessage("task-a", "I finished the auth refactor")
session.SendMessage(msg)

// Via pool — uses sender's SessionMeta.Name automatically
pool.SendAgentMessage(s1.SessionID(), s2.SessionID(), "I finished the auth refactor")
```

### Typed Agent tool input

When a session spawns a sub-agent, the `ToolUseEvent` has `Name: "Agent"`. Use `ParseAgentInput()` to extract structured fields without manual JSON parsing:

```go
case *claudecli.ToolUseEvent:
    if agent := e.ParseAgentInput(); agent != nil {
        fmt.Printf("Agent: %s (%s) — %s\n", agent.Name, agent.SubagentType, agent.Description)
        if agent.Isolation == "worktree" {
            fmt.Println("  running in isolated git worktree")
        }
    }
```

`AgentInput` fields: `Description`, `Prompt`, `SubagentType`, `Name`, `RunInBackground`, `Model`, `Isolation` (`"worktree"` for git worktree isolation), `Mode` (permission mode: `"plan"`, `"acceptEdits"`, etc.), `TeamName` (team name for spawning).

### Subagent activity tracking

`UserEvent` makes subagent execution visible. Use `ParentToolUseID` to correlate events with their parent Agent tool call, and `AgentResult` to detect completion:

```go
case *claudecli.UserEvent:
    if e.ParentToolUseID != "" {
        // This event belongs to the subagent spawned by that Agent tool call.
        fmt.Printf("  [subagent %s] tool result\n", e.ParentToolUseID)
    }
    if e.AgentResult != nil {
        fmt.Printf("  Agent %s (%s) completed: %d tokens, %dms, %d tool calls\n",
            e.AgentResult.AgentID, e.AgentResult.AgentType,
            e.AgentResult.TotalTokens, e.AgentResult.TotalDurationMs,
            e.AgentResult.TotalToolUseCount)
    }
case *claudecli.TaskEvent:
    // Real-time subagent lifecycle: task_started → task_progress → task_notification
    fmt.Printf("  [task %s] %s (tokens: %d, tools: %d, %dms)\n",
        e.Subtype, e.Description, e.TotalTokens, e.ToolUses, e.DurationMs)
case *claudecli.ToolUseEvent:
    if e.ParentToolUseID != "" {
        fmt.Printf("  [subagent] tool: %s\n", e.Name) // from a subagent
    } else {
        fmt.Printf("[tool: %s]\n", e.Name) // top-level
    }
```

### Per-subagent model

The model a subagent actually ran on is carried on the subagent's own assistant
messages, and nowhere else. It arrives as the **resolved** API model id, which
is more specific than the alias in the Agent tool's input — `AgentInput.Model`
is one of `sonnet|opus|haiku|fable`, and is empty when the subagent inherits
the parent's model.

Correlate by matching `ParentToolUseID` against the `ToolUseID` the
`task_started` `TaskEvent` carried:

```go
models := map[string]string{} // taskID -> resolved model
toolUseToTask := map[string]string{}

for ev := range stream.Events() {
    switch e := ev.(type) {
    case *claudecli.TaskEvent:
        if e.Subtype == "task_started" {
            toolUseToTask[e.ToolUseID] = e.TaskID
            fmt.Printf("task %s: %s subagent\n", e.TaskID, e.SubagentType)
        }
    case *claudecli.TextEvent:
        if e.ParentToolUseID == "" {
            continue // parent conversation, not a subagent
        }
        if taskID, ok := toolUseToTask[e.ParentToolUseID]; ok && e.Model != "" {
            models[taskID] = e.Model // e.g. "claude-haiku-4-5-20251001"
        }
    }
}
```

**This requires `WithForwardSubagentText()`.** Without it the CLI emits no
subagent assistant messages at all, so `Model`, `SubagentType` and
`TaskDescription` are always empty — the task events still arrive, but none of
them carry a model. That is the CLI's only path to this data: there is no way
to learn a subagent's model without also receiving its forwarded transcript.

Effort is **not** available per subagent — see
[Known limitations](#known-limitations--todo).

## Dynamic workflows

[Dynamic workflows](https://code.claude.com/docs/en/workflows) orchestrate many
subagents from a script the CLI runtime runs in the background. There is no new
flag or API to trigger one — it is prompt content: include `ultracode`, ask in
your own words ("run a workflow…"), or invoke a saved/bundled command like
`/deep-research`. In headless mode (`-p`) the run starts automatically.

A workflow surfaces through the ordinary event stream as a single synthetic task
with `TaskType == "local_workflow"`. The run is a **two-turn lifecycle** that
emits **two `ResultEvent`s**: the first says the workflow is running in the
background, the second (after it completes) carries the real answer — so **take
the last `ResultEvent`**, not the first.

```go
for event := range stream.Events() {
    switch e := event.(type) {
    case *claudecli.UserEvent:
        if wl := e.WorkflowLaunch; wl != nil {
            fmt.Printf("workflow %q launched (run %s)\n", wl.WorkflowName, wl.RunID)
        }
    case *claudecli.TaskEvent:
        if e.IsWorkflow() {
            for _, p := range e.WorkflowProgress { // per-phase / per-agent state
                if p.IsAgent() {
                    fmt.Printf("  [%s] %s: %s (%d tok)\n", p.PhaseTitle, p.Label, p.State, p.Tokens)
                }
            }
        }
    case *claudecli.ResultEvent:
        fmt.Println("answer:", e.Text) // keep the LAST one
    }
}
```

The CLI stamps `task_type` only on the `task_started` event; the later
`task_progress`, `task_updated`, and `task_notification` events for the same
task omit it. The SDK backfills `TaskType` and `WorkflowName` from the matching
`task_started`, so `IsWorkflow()` and `WorkflowName` stay correct across the
whole lifecycle — you can gate on `IsWorkflow()` for progress and the terminal
`task_notification` too, not just the launch.

### Monitoring out-of-band

The runtime also persists live run state on disk keyed by `RunID` (it survives
the SDK's `--no-session-persistence`). Given a `WorkflowLaunch`, you can monitor
the run from a separate goroutine without consuming the event stream:

```go
launch := userEvent.WorkflowLaunch
snaps, err := claudecli.WatchWorkflow(ctx, launch,
    claudecli.WithPollInterval(time.Second))
for snap := range snaps { // closes on terminal status (completed/stopped/killed)
    fmt.Printf("%s: %d/%d agents, %d tokens\n",
        snap.Status, len(snap.Agents()), snap.AgentCount, snap.TotalTokens)
}

// Or a one-shot read (e.g. to fetch the final Result after completion):
snap, err := claudecli.ReadWorkflowSnapshot(launch)
```

`WorkflowLaunch.ManifestPath()` / `JournalPath()` expose the underlying file
paths. This reads an **undocumented internal CLI layout** that may change between
versions, so it degrades gracefully (transient read/parse errors are retried;
`Raw` preserves the full manifest). The in-stream `WorkflowProgress` carries the
same live data, so the filesystem path is a complement for out-of-band or
fire-and-forget monitoring, not a requirement.

## Multimodal input

Send images and documents alongside text in interactive sessions:

```go
imgData, _ := os.ReadFile("screenshot.png")
session.QueryWithContent("Describe this image",
    claudecli.ImageBlock("image/png", imgData),
)

pdfData, _ := os.ReadFile("report.pdf")
session.QueryWithContent("Summarize this document",
    claudecli.DocumentBlock("application/pdf", pdfData),
)
```

Content block constructors: `TextBlock`, `ImageBlock`, `DocumentBlock`. Base64 encoding is handled internally.

## Custom executor

The `Executor` interface controls how the CLI process is spawned. Implement it to run Claude in Docker, over SSH, or any other environment.

```go
type Executor interface {
    Start(ctx context.Context, cfg *StartConfig) (*Process, error)
}

type StartConfig struct {
    Args                    []string
    Stdin                   io.Reader
    Env                     map[string]string
    WorkDir                 string
    KeepStdinOpen           bool
    EnableFileCheckpointing bool
}
```

```go
// Example: run Claude inside a Docker container
type DockerExecutor struct {
    Image  string
    Mounts []string
}

func (d *DockerExecutor) Start(ctx context.Context, cfg *claudecli.StartConfig) (*claudecli.Process, error) {
    dockerArgs := []string{"run", "--rm", "-i", d.Image}
    dockerArgs = append(dockerArgs, "claude")
    dockerArgs = append(dockerArgs, cfg.Args...)
    cmd := exec.CommandContext(ctx, "docker", dockerArgs...)
    cmd.Stdin = cfg.Stdin
    if cfg.WorkDir != "" {
        cmd.Dir = cfg.WorkDir
    }
    // ... set up stdout/stderr pipes ...
    cmd.Start()
    return &claudecli.Process{
        Stdout: stdout,
        Stderr: stderr,
        Wait:   cmd.Wait,
    }, nil
}

client := claudecli.NewWithExecutor(&DockerExecutor{Image: "my-claude:latest"},
    claudecli.WithModel(claudecli.ModelSonnet),
)
```

## Testing

Use `FixtureExecutor` to replay recorded JSONL streams without invoking the real CLI:

```go
func TestMyFeature(t *testing.T) {
    exec, err := claudecli.NewFixtureExecutorFromFile("testdata/session.jsonl")
    if err != nil {
        t.Fatal(err)
    }
    client := claudecli.NewWithExecutor(exec)

    text, _, err := client.RunText(context.Background(), "ignored prompt")
    if err != nil {
        t.Fatal(err)
    }
    if text != "expected output" {
        t.Errorf("got %q", text)
    }
}
```

For testing interactive sessions, use `BidiFixtureExecutor`:

```go
bidi := claudecli.NewBidiFixtureExecutor()
client := claudecli.NewWithExecutor(bidi)

go func() {
    // Simulate CLI responses on bidi.StdoutWriter
    // Read SDK requests from bidi.StdinReader
    bidi.StdoutWriter.Write([]byte(`{"type":"system","session_id":"test","model":"sonnet"}` + "\n"))
    bidi.StdoutWriter.Close()
}()

session, _ := client.Connect(ctx)
```

You can also parse JSONL directly:

```go
ch := make(chan claudecli.Event, 64)
go func() {
    defer close(ch)
    claudecli.ParseEvents(ctx, reader, ch)
}()
for event := range ch {
    // ...
}
```

## Event types

All events implement the sealed `Event` interface. Use type switches or type assertions.

| Type               | Description                                                                                                                 |
| ------------------ | --------------------------------------------------------------------------------------------------------------------------- |
| `*StartEvent`      | Emitted before process launch. Contains resolved model, args, working dir.                                                  |
| `*InitEvent`       | CLI session started. Session ID, model, available tools, agents, skills, MCP servers. `ModelDisplayName()` renders the model ID as e.g. `"Opus 5"`. Also carries `CLIVersion`, `CWD`, `PermissionMode` (the mode actually in effect), `OutputStyle`, `SlashCommands`, `Plugins` (`[]PluginInfo`), and `MCPServerErrors` (`[]MCPServerError` — `--mcp-config` entries skipped by validation, which never appear in `MCPServers`; requires CLI 2.1.219+). |
| `*CompactStatusEvent` | Compaction status change. `Status` is `"compacting"` or `""` (cleared).                                                  |
| `*CompactBoundaryEvent` | Compaction boundary marker. `Trigger` (`"manual"`/`"auto"`), `PreTokens`, `Raw` metadata.                              |
| `*TaskEvent`       | Subagent lifecycle update (system subtypes `task_started`, `task_progress`, `task_updated`, `task_notification`). `ToolUseID` links to the parent Agent call. Fields: `TaskID`, `Description`, `TaskType`, `Prompt`, `LastToolName`, `Status`, `Summary`, `TotalTokens`, `ToolUses`, `DurationMs`, `EndTime`, `SubagentType`. `IsWorkflow()` is true for dynamic-workflow runs (`TaskType == "local_workflow"`), where `WorkflowName`, `WorkflowProgress` (per-phase/per-agent `[]WorkflowProgressEntry`), and `OutputFile` (on completion) are also set. See [Dynamic workflows](#dynamic-workflows). |
| `*HookEvent`       | Hook lifecycle event (system subtypes `hook_started`, `hook_progress`, `hook_response`). Requires `WithIncludeHookEvents()` — the CLI emits nothing otherwise. Fields: `HookID`, `HookName`, `HookEvent` (e.g. `"SessionStart"`), and on `hook_response`: `Output`, `Stdout`, `Stderr`, `ExitCode`, `Outcome`. |
| `*ThinkingEvent`   | Model thinking output. Includes `Signature` for verification. `Content` may be empty while `Signature` is set — treat `Content=="" && Signature!=""` as "thinking hidden", not "no thinking". `ParentToolUseID` set when from a subagent, plus `Model`/`SubagentType`/`TaskDescription` — see [Per-subagent model](#per-subagent-model). |
| `*TextEvent`       | Assistant text output. `ParentToolUseID` set when from a subagent, plus `Model`/`SubagentType`/`TaskDescription` — see [Per-subagent model](#per-subagent-model). |
| `*TurnEvent`       | New assistant turn started. `Turn` is a 1-based counter, `ToolName` is the first tool in the turn (empty for text-only turns). Only emitted for top-level turns (subagent messages excluded). |
| `*ToolUseEvent`    | Tool invocation with name and input. `ParseAgentInput()` returns typed `*AgentInput` for Agent tool calls. `ParentToolUseID` set when from a subagent, plus `Model`/`SubagentType`/`TaskDescription` — see [Per-subagent model](#per-subagent-model). `ServerSide` is true for server-side tools (web search, code execution). `MCP` is true for MCP tool calls. |
| `*ToolResultEvent` | Result from a tool invocation. `Content` is `[]ToolContent` supporting text and image blocks. `Text()` returns concatenated text. `ParentToolUseID` set when from a subagent. |
| `*UserEvent`       | Tool result or subagent message fed back to the model. `Content` is `[]UserContent` (text or tool_result blocks). `ParentToolUseID` links subagent events to the parent Agent tool call (empty for top-level). `AgentResult` (non-nil on subagent completion) carries `AgentID`, `AgentType`, `Prompt`, `TotalDurationMs`, `TotalTokens`, `TotalToolUseCount`. `WorkflowLaunch` (non-nil when a dynamic workflow is launched in the background) carries `RunID`, `WorkflowName`, `ScriptPath`, `TranscriptDir` and helpers for out-of-band monitoring — see [Dynamic workflows](#dynamic-workflows). `IsReplay` is true when echoed via `--replay-user-messages`. `Text()` returns concatenated text. |
| `*UnknownEvent`    | Unrecognized event type from CLI. `Type` is the raw type string (or `"content/<type>"` for unknown content blocks), `Raw` is the full JSON. Forward-compat catch-all — also used for error fallback diagnostics on non-zero exit. |
| `*RateLimitEvent`  | Rate limit status change. Fields: `Status`, `Utilization`, `ResetsAt`, `RateLimitType`, overage fields, `UUID`, `SessionID`, `Raw`. |
| `*StderrEvent`     | A line of stderr output from the CLI process.                                                                               |
| `*ResultEvent`     | Session complete. Text, cost, duration, usage, `NumTurns`, `StopReason`, `StructuredOutput`, `ModelUsage` (per-model context window, token limits, web search/fetch counts), `ContextSnapshot` (per-API-call usage from last `message_start`/`message_delta`; requires `WithIncludePartialMessages`; nil otherwise). Synthesized if CLI exits cleanly without one. |
| `*ContextManagementEvent` | Emitted when the CLI compresses or summarizes older turns to fit the context window. `Raw` contains the full JSON payload. |
| `*ThinkingTokensEvent` | Running estimate of thinking-token usage during a turn (system subtype `thinking_tokens`). `EstimatedTokens` (cumulative) and `EstimatedTokensDelta` (increment). A progress signal, not authoritative accounting — use `ResultEvent.Usage` for final counts. |
| `*CLIStateChangeEvent` | Activity-state transition (`idle` / `thinking` / `awaiting_tool_result`). Emitted immediately BEFORE the triggering event so consumers can flip their state before processing the event. Lets watchdogs distinguish "model generating" from "CLI running a tool" without inferring pairing from `ToolUseEvent`/`ToolResultEvent`. Backward-compatible: ignore in the type switch if unused. |
| `*ToolProgressEvent` | Periodic heartbeat (every 30 s) while in `awaiting_tool_result`. Carries `ToolUseID`, `ToolName`, and `Elapsed` for the first pending top-level tool_use (stable across parallel tool_use calls). Pushed liveness signal so consumers don't poll `ProcessInfo()` to render "Bash running for 4m 12s". Backward-compatible: ignore in the type switch if unused. |
| `*CLIToolProgressEvent` | Tool progress event from the CLI JSONL stream (top-level `tool_progress` type). Unlike the synthetic `ToolProgressEvent`, this comes directly from the CLI and carries `ElapsedSeconds` and optional `TaskID`. |
| `*ToolUseSummaryEvent` | Emitted after tool execution with a human-readable summary. `PrecedingToolUseIDs` lists the tool_use IDs covered. |
| `*AuthStatusEvent` | Authentication status change during a session (e.g. token refresh). `IsAuthenticating`, `Output`, `Error`. |
| `*PromptSuggestionEvent` | Predicted next user prompt, emitted after each turn when `WithPromptSuggestions()` is set. Sessions only — the CLI emits it after the turn's result, which one-shot `Run` treats as terminal. Advisory: a guess at what the user might ask next, not an instruction. |
| `*FilesPersistedEvent` | File persistence confirmation. `Files` lists successfully persisted files (`Filename`, `FileID`); `Failed` lists failures. |
| `*BackgroundTasksChangedEvent` | The complete set of live background tasks after any membership change (`Tasks []BackgroundTask`). **REPLACE semantics** — swap your set for the payload. A *level* signal, unlike the `task_started`/`task_notification` *edge* pair: a missed edge wedges a stale "running" indicator, so consumers that only need "is background work running" should read it here. Nothing is emitted at startup, so reset to empty when the CLI process restarts. |
| `*CommandsChangedEvent` | The full slash-command list after a mid-session change (`Commands []SlashCommand`), e.g. skills discovered dynamically or a `ReloadSkills()` call. **REPLACE semantics** — swap your cached list for the payload. |
| `*SessionStateChangedEvent` | The CLI's own session state: `SessionStateIdle`, `SessionStateRunning`, or `SessionStateRequiresAction`. The only signal that distinguishes "waiting on the user" from "idle". |
| `*ConversationResetEvent` | The conversation was reset by `/clear`, plan-mode exit, or a fresh-session flow. A transcript boundary, not a session restart: the session ID is unchanged but the model's context is gone. Mount a fresh transcript under `NewConversationID` and drop any cached title. |
| `*PermissionDeniedEvent` | A tool call denied without an interactive prompt (auto-mode classifier, `dontAsk`, deny rules, headless auto-deny). Carries `ToolName`, `ToolUseID`, `AgentID`, `DecisionReasonType`, `DecisionReason`, `Message`. Advisory and best-effort. |
| `*ControlRequestEvent` | Control request from CLI (handled internally in sessions).                                                              |
| `*StreamEvent`     | Partial message update (when `WithIncludePartialMessages` is on).                                                            |
| `*ErrorEvent`      | Error during streaming. `Fatal` field distinguishes process failures (which set `StateFailed`) from non-fatal errors (parse errors, API errors). API errors are classified via `errors.Is` with sentinel errors (see error handling below). |
| `*CLIExitEvent`    | Final event before the events channel closes (Session mode). `Reason` (`"normal"` / `"killed"` / `"crashed"` / `"context_canceled"` / `"unknown"`), `ExitCode` (-1 if signaled or non-`*exec.ExitError`), `Signal` (e.g. `"SIGKILL"`, empty if not signaled), `Err` (underlying `*Error` or context error), `At`. Lets consumers give actionable termination messages instead of inferring cause from a closed channel. Backward-compatible: ignore in the type switch if unused. |

## Options

| Option                               | Description                                                                                           |
| ------------------------------------ | ----------------------------------------------------------------------------------------------------- |
| `WithBinaryPath(string)`             | Path to the `claude` binary. Only effective in `New()`. Default: `"claude"`.                          |
| `WithModel(Model)`                   | Model to use (`ModelHaiku`, `ModelSonnet`, `ModelOpus`, `ModelFable`). Default: `ModelSonnet`. These constants are bare aliases, so the CLI resolves each to the latest release of that tier — as of CLI 2.1.227, `ModelOpus` → `claude-opus-5`, `ModelSonnet` → `claude-sonnet-5`, `ModelFable` → `claude-fable-5`, `ModelHaiku` → `claude-haiku-4-5-20251001`. Pin a version only for reproducibility, by passing the full ID as a string: `Model("claude-opus-5")`. |
| `WithFallbackModel(Model)`           | Fallback model if primary is unavailable.                                                             |
| `WithBetas(...string)`               | Beta features to enable.                                                                              |
| `WithSystemPrompt(string)`           | System prompt.                                                                                        |
| `WithSystemPromptFile(string)`       | Load system prompt from a file.                                                                       |
| `WithAppendSystemPrompt(string)`     | Append to the default system prompt.                                                                  |
| `WithAppendSystemPromptFile(string)` | Append to the default system prompt from a file.                                                      |
| `WithTools(...string)`               | Allowed tools. Accepts individual names or comma-separated (`"A,B"` == `"A", "B"`). Deduplicates.     |
| `WithDisallowedTools(...string)`     | Disallowed tools. Same comma/dedup behavior as `WithTools`.                                           |
| `WithBuiltinTools(...string)`        | Restrict available built-in tools. `"default"` for all, `""` for none, or names like `"Bash"`, `"Edit"`. |
| `WithPermissionMode(PermissionMode)` | Permission mode (`PermissionDefault`, `PermissionPlan`, `PermissionAcceptEdits`, `PermissionBypass`, `PermissionDontAsk`, `PermissionAuto`, `PermissionManual`). |
| `WithDangerouslySkipPermissions()`   | Bypass all permission checks. Emits both `--allow-dangerously-skip-permissions` and `--dangerously-skip-permissions`. Only for sandboxed environments. |
| `WithBare()`                         | Minimal mode: skip hooks, LSP, plugin sync, attribution, auto-memory, background prefetches, keychain reads, CLAUDE.md auto-discovery. |
| `WithJSONSchema(string)`             | JSON schema for structured output validation.                                                         |
| `WithMaxBudget(float64)`             | Maximum cost budget in USD.                                                                           |
| `WithMaxTurns(int)`                  | Maximum agentic turns before stopping.                                                                |
| `WithWorkDir(string)`                | Working directory for the CLI process.                                                                |
| `WithAddDirs(...string)`             | Additional directories to allow tool access to.                                                       |
| `WithSessionID(string)`              | Resume a specific session.                                                                            |
| `WithSessionName(string)`            | Display name for the session (shown in `/resume` and terminal title).                                 |
| `WithForkSession()`                  | Fork from the session (requires `WithSessionID`).                                                     |
| `WithContinue()`                     | Continue the most recent session.                                                                     |
| `WithEffort(EffortLevel)`            | Reasoning effort (`EffortLow`, `EffortMedium`, `EffortHigh`, `EffortXHigh`, `EffortMax`). `DefaultEffort` is `EffortXHigh`.                                |
| `WithThinking(ThinkingConfig)`       | Extended thinking mode. Use `ThinkingAdaptive{}` (emits `--thinking adaptive`), `ThinkingEnabled{BudgetTokens: N}` (emits `--max-thinking-tokens N`), or `ThinkingDisabled{}` (emits `--thinking disabled`). Overlaps with `WithEffort`; prefer `WithEffort` unless explicit control is needed. |
| `WithTaskBudget(int)`                | Cap total tokens per task. Emits `--task-budget`. Zero is ignored.                                    |
| `WithMCPConfig(...string)`           | MCP server configs — file paths or inline JSON strings.                                               |
| `WithStrictMCPConfig()`              | Only use MCP servers from `WithMCPConfig`, ignoring all other MCP configurations.                     |
| `WithAgent(string)`                  | Named agent for the session.                                                                          |
| `WithAgentDef(string)`               | Custom agent definitions as JSON.                                                                     |
| `WithIncludePartialMessages()`       | Include partial message chunks (streaming only).                                                      |
| `WithSettings(string)`               | Path to settings file.                                                                                |
| `WithSettingSources(...string)`      | Setting sources (comma-joined).                                                                       |
| `WithPluginDirs(...string)`          | Plugin directories.                                                                                   |
| `WithResume(string)`                 | Resume a session by ID (mutually exclusive with `WithSessionID`/`WithContinue`).                      |
| `WithCanUseTool(ToolPermissionFunc)` | Tool permission callback (sessions only). Receives only the tool name and input.                      |
| `WithCanUseToolRequest(ToolPermissionRequestFunc)` | Tool permission callback receiving the full `ToolPermissionRequest` — `ToolUseID`, `AgentID`, `DecisionReason`/`DecisionReasonType`, `PermissionSuggestions`, and presentation fields. Needed to attribute a prompt to the call or subagent that raised it, and to support "always allow" via `PermissionResponse.UpdatedPermissions`. Takes precedence over `WithCanUseTool`. |
| `WithCanUseToolRequestContext(ToolPermissionRequestContextFunc)` | Same as `WithCanUseToolRequest`, plus a per-request `context.Context` that is cancelled when the CLI withdraws the prompt or the session ends — the only way a callback learns its answer will be discarded. Use it whenever the decision parks on a human. Takes precedence over both other variants. |
| `WithUserInput(UserInputFunc)`       | Dedicated callback for `AskUserQuestion` tool requests (sessions only).                               |
| `WithUserInputContext(UserInputContextFunc)` | Same as `WithUserInput`, plus the per-request `context.Context` described above. Takes precedence over `WithUserInput`. |
| `WithControlTimeout(time.Duration)` | Timeout for control protocol round-trips (default: 30s). Sessions only.                               |
| `WithInitTimeout(time.Duration)`   | Timeout for the initialize handshake (default: 60s). Increase if MCP servers are slow to connect. Sessions only. |
| `WithStdinWriteTimeout(time.Duration)` | Deadline for individual stdin writes (default: 30s). A write that blocks past it means the CLI stopped reading stdin: the write fails, stdin is closed permanently and the session must be recycled. Keeps `Close()` from deadlocking against a blocked write. Sessions only. |
| `WithPermissionPromptToolName(string)` | Custom permission prompt tool name (default: `"stdio"`). Sessions only.                             |
| `WithEnv(map[string]string)`         | Additional environment variables. Can override `CLAUDE_CODE_ENTRYPOINT` (default: `"sdk-go"`).        |
| `WithExtraArgs(map[string]string)`   | Arbitrary `--key value` flags for forward compatibility. Empty value emits flag only.                  |
| `WithUser(string)`                   | **Deprecated, no-op.** The CLI removed `--user`; passing it made the CLI exit with `unknown option '--user'`. Kept only so existing callers compile. |
| `WithSafeMode()`                     | Disable all customizations (CLAUDE.md, skills, plugins, hooks, MCP servers, custom commands/agents, output styles, workflows). Policy settings, auth, model selection, built-in tools, and permissions still work. |
| `WithAutoCompact(string)`            | Auto-compact window size: `"auto"`, or a token count between 100k and 1M (e.g. `"200000"`).           |
| `WithExcludeDynamicSystemPromptSections()` | Move per-machine sections (cwd, env info, memory paths, git status) out of the system prompt into the first user message, improving prompt-cache reuse across machines. Ignored when `WithSystemPrompt` is set. |
| `WithPluginURLs(...string)`          | Fetch plugin `.zip` archives from URLs for this session only.                                          |
| `WithIncludeHookEvents()`            | Surface hook lifecycle activity as `*HookEvent`. **Required** to receive any hook events — without it the CLI emits none. Streaming and sessions only. |
| `WithForwardSubagentText()`          | Forward subagent text and thinking as `*TextEvent`/`*ThinkingEvent` with `ParentToolUseID` set. Includes nested subagents (depth 2+). Streaming and sessions only. |
| `WithPromptSuggestions()`            | Emit a `*PromptSuggestionEvent` with a predicted next user prompt after each turn. **Sessions only** — the CLI emits it after the turn's result, which one-shot `Run` treats as terminal. |
| `WithStderrCallback(func(string))`   | Called per stderr line in addition to `StderrEvent` emission.                                         |
| `WithDebugFile(string)`              | Write CLI debug logs to a file path.                                                                  |
| `WithDisableSlashCommands()`         | Disable all slash command / skill processing in prompts.                                              |
| `WithFileCheckpointing()`            | Enable SDK file checkpointing via `CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING` env var.                |
| `WithReplayUserMessages()`           | Echo user messages back on stdout as `UserEvent` with `IsReplay=true`, confirming message delivery. Useful for tracking `SendMessage` acknowledgment during active turns. Sessions only. |

Options set at call time **replace** (not merge with) client-level defaults.

## Error handling

```go
import "errors"

text, _, err := client.RunText(ctx, prompt)

// Empty output (no text events received)
if errors.Is(err, claudecli.ErrEmptyOutput) { ... }

// Classify API errors with sentinel errors
if errors.Is(err, claudecli.ErrInvalidRequest) { ... }  // 400 bad request
if errors.Is(err, claudecli.ErrAuth) { ... }             // 401 authentication
if errors.Is(err, claudecli.ErrBilling) { ... }           // 402 billing/payment
if errors.Is(err, claudecli.ErrPermission) { ... }        // 403 permission denied
if errors.Is(err, claudecli.ErrNotFound) { ... }          // 404 not found
if errors.Is(err, claudecli.ErrRequestTooLarge) { ... }   // 413 request too large
if errors.Is(err, claudecli.ErrRateLimit) { ... }         // 429 rate limited
if errors.Is(err, claudecli.ErrAPI) { ... }               // 500 internal API error
if errors.Is(err, claudecli.ErrOverloaded) { ... }        // 529 API overloaded
if errors.Is(err, claudecli.ErrMaxTurns) { ... }          // max turns reached
if errors.Is(err, claudecli.ErrContextWindowExceeded) { ... } // context window exceeded

// Extract turn count from max turns errors
var mte *claudecli.MaxTurnsError
if errors.As(err, &mte) {
    fmt.Printf("hit max turns: %d\n", mte.Turns)
}

// Extract retry timing from rate limit errors
var rlErr *claudecli.RateLimitError
if errors.As(err, &rlErr) {
    time.Sleep(rlErr.RetryAfter)
}

// CLI process failure with exit code and stderr
// Error.Error() returns a concise message (auto-inferred from stderr patterns
// like "command not found", "permission denied", "no such file or directory").
// For full stderr, access the Stderr field directly.
// LastEvents contains the last 10 raw JSONL lines from stdout for post-mortem
// diagnostics when stderr and classified errors yield no information.
var cliErr *claudecli.Error
if errors.As(err, &cliErr) {
    fmt.Println(cliErr.ExitCode)
    fmt.Println(cliErr.Message)    // concise, auto-inferred from stderr
    fmt.Println(cliErr.Stderr)     // full stderr output
    fmt.Println(cliErr.LastEvents) // last 10 raw JSONL lines from stdout
}

// RunJSON/RunBlockingJSON failed to parse response as JSON
var ue *claudecli.UnmarshalError
if errors.As(err, &ue) {
    fmt.Println(ue.RawText) // original model output before fence stripping
}
```

## Architecture

```
claudecli-go/
  doc.go         Package overview, thread safety, prerequisites
  event.go       Sealed Event interface, event types
  model.go       Model constants, EffortLevel constants (including DefaultEffort)
  permission.go  PermissionMode constants
  option.go      Functional options + CLI arg builder
  executor.go         Executor interface, LocalExecutor, FixtureExecutor, BidiFixtureExecutor
  executor_unix.go    Unix process group attrs (Setpgid, SIGTERM), stdbuf wrapping
  executor_windows.go Windows no-op platform attrs
  parse.go       JSONL stream parser (decoupled from process lifecycle)
  stream.go      Stream with State(), Events(), Next(), Wait(), Close()
  client.go      Client struct, Run/RunText/RunJSON/Connect, package-level shortcuts
  session.go     Interactive session with bidirectional control protocol
  control.go     Control message types, ContentBlock/ImageSource for multimodal input
  blocking.go    RunBlocking/RunBlockingJSON — non-streaming JSON output mode
  auth.go        AuthStatus (defensive three-state parsing), AuthLogin (BROWSER capture + localhost callback), AuthLogout, LoginProcess
  pool.go        Pool multi-session registry, FormatAgentMessage, SendAgentMessage
  version.go     sdkVersion (module version reported to CLI, build-info sourced), SDKVersion dev fallback, MinCLIVersion, CLI version checking with semver parsing
  install.go     DetectInstall — read-only install-method detection (npm/native/package-manager/version-manager) and the matching update command
  internal.go    Stderr ring buffer, processExitError with heuristic inference, code fence stripping
  error.go       Sentinel errors (ErrInvalidRequest, ErrAuth, ErrBilling, ErrPermission, ErrNotFound, ErrRequestTooLarge, ErrRateLimit, ErrAPI, ErrOverloaded, ErrMaxTurns, ErrContextWindowExceeded), RateLimitError, MaxTurnsError, Error, UnmarshalError
```

**Layers:**

1. **Parse** (`parse.go`) — JSONL deserialization into typed events. Zero coupling to process execution. Testable with fixtures. Returns immediately after the result event to avoid blocking on CLI hang bugs.
2. **Execute** (`executor.go`, `executor_{unix,windows}.go`) — `Executor` interface abstracts process spawning. `LocalExecutor` handles the real CLI with platform-aware command construction: `stdbuf -oL` wrapping on Linux, npm `.cmd` shim bypass on Windows.
3. **Client** (`client.go`) — Composes executor + options. Builds CLI args, starts process synchronously, reads events in goroutine. Synthesizes `ResultEvent` if CLI exits without one. `Connect()` creates interactive sessions.
4. **Session** (`session.go`) — Bidirectional control protocol over stdin/stdout. Handles initialize handshake, control request routing (tool permissions), and multi-turn conversations. `Connect()` marks the session ready immediately after the initialize handshake (CLI 2.1.81+ defers the system init event until the first user message).
5. **Blocking** (`blocking.go`) — Non-streaming path using `--output-format json`. Simpler execution model for `RunBlocking`/`RunBlockingJSON`.

## Known limitations / TODO

- **JSONL format is unversioned** — Claude CLI's `stream-json` output format is not formally versioned by Anthropic. Tested with Claude Code CLI 2.x. Breaking changes across CLI versions are possible.
- **No retry/backoff** — `RateLimitEvent` is emitted (with `ResetsAt` timestamp and `RateLimitType`) but the package does not automatically retry or backoff. Consumers must implement their own retry logic.
- **`stdbuf` recommended on Linux** — `LocalExecutor` uses `stdbuf -oL` for line-buffered stdout on Linux when available, falling back to direct execution without it.
- **MCP server startup can be slow** — The CLI waits for MCP server connections during the initialize handshake. With many MCP servers configured, this can take 30+ seconds. The `WithInitTimeout` option (default 60s) controls this; increase it if `Connect()` times out.
- **`WithExtraArgs` validates reserved flags** — Passing `print`, `output-format`, `input-format`, or `verbose` via `WithExtraArgs` panics at construction time to prevent conflicting CLI arguments.
- **Blocking stderr capped at 10 MB** — `RunBlocking` caps stderr collection at 10 MB. The streaming path uses a 1000-line ring buffer.
- **Fork-session needs a persisted parent** — `RunBlocking` by default emits `--no-session-persistence`, so the parent must be started with `WithSessionID`, `WithResume`/`WithContinue`, or via `Connect` for `WithForkSession` to find the parent on disk.
- **`AuthStatus` fail-close** — When the CLI exits 0 with non-JSON output, `AuthStatus` returns `AuthStateUnknown` (not `AuthStateAuthenticated`). Callers should handle this explicitly.
- **`DetectInstall` on Windows is unverified** — the layouts are implemented
  from npm/winget conventions but have not been exercised on real Windows
  hardware. `.exe` binaries and npm's `node_modules` layout are handled; a
  `.cmd`/`.bat`/`.ps1` shim is not a symlink and cannot be resolved further, so
  unless a sibling npm layout confirms the install it reports `InstallUnknown`
  rather than guessing. `DetectInstall` also has no command for `asdf`, `deb`,
  `rpm`, `pacman` or `apk` installs — it reports `package-manager` with an empty
  `UpdateCmd`, matching the CLI, which does not know one either.
- **Thinking text is withheld by default** — By default the CLI emits
  `ThinkingEvent`s with empty `Content` but a set `Signature`: the model
  thought, the reasoning is cryptographically attested, but the text is
  withheld. Distinguish "thinking hidden" from "no thinking" via
  `Content == "" && Signature != ""`. See
  [Reading thinking text](#reading-thinking-text) for how to opt in — and the
  two conditions that have to hold.
- **Per-subagent effort is not observable** — the CLI carries a subagent's
  resolved model on its assistant messages (see
  [Per-subagent model](#per-subagent-model)) but nothing in the stream carries
  its effort level. The only available value is whatever the Agent tool input
  specified, which is absent when the subagent inherits the parent's effort.
  This is an upstream gap, not an SDK one.
- **Per-subagent model costs a forwarded transcript** — obtaining it requires
  `WithForwardSubagentText()`, which forwards every subagent text and thinking
  block. There is no cheaper path to the model id.
- **Hooks are not supported** — `WithIncludeHookEvents()` surfaces hook
  *lifecycle* events, but the SDK cannot register hook callbacks
  (`initialize.hooks` / the inbound `hook_callback` control request). Inbound
  control requests other than `can_use_tool` are answered with an
  "unsupported control request" error; this also covers SDK-hosted MCP servers
  (`mcp_message`), MCP elicitations, and tool-driven dialogs
  (`request_user_dialog`).
- **Workflows emit two `ResultEvent`s** — A [dynamic workflow](#dynamic-workflows) run produces two result events: the first reports it launched in the background, the second carries the real answer once it completes. Consume the **last** `ResultEvent`. A workflow also does not survive its parent CLI process (the run settles at `status: "killed"`).
- **Workflow on-disk state is an internal layout** — `WatchWorkflow`/`ReadWorkflowSnapshot` read CLI run-state files (`~/.claude/projects/.../workflows/<runId>.json`) whose paths and JSON shape are undocumented and may change across CLI versions. They parse defensively and preserve `Raw`, but treat this as best-effort. File GC/lifetime is unverified.
