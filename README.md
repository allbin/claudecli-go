# claudecli-go

Go package for invoking the [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code) as a subprocess with typed streaming events, functional options, and pluggable execution.

**Requires**: `claude` CLI installed and on PATH.

## Install

```
go get github.com/allbin/claudecli-go
```

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
    fmt.Printf("Cost: $%.4f, Tokens: %d in / %d out\n",
        result.CostUSD, result.Usage.InputTokens, result.Usage.OutputTokens)
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
        // extended thinking output
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
- `SetPermissionMode(mode)` — change permissions mid-session
- `SetModel(model)` — change model mid-session
- `GetServerInfo()` — raw JSON from the initialize handshake
- `RewindFiles(userMessageID)` — rewind files to a checkpoint
- `ReconnectMCPServer(name)` — reconnect a named MCP server
- `ToggleMCPServer(name, enabled)` — enable/disable an MCP server
- `StopTask(taskID)` — stop a running task
- `GetMCPStatus()` — query MCP server status
- `Close()` — terminate session

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
    claudecli.ParseEvents(reader, ch)
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
| `*InitEvent`       | CLI session started. Session ID, model, available tools.                                                                    |
| `*CompactStatusEvent` | Compaction status change. `Status` is `"compacting"` or `""` (cleared).                                                  |
| `*CompactBoundaryEvent` | Compaction boundary marker. `Trigger` (`"manual"`/`"auto"`), `PreTokens`, `Raw` metadata.                              |
| `*ThinkingEvent`   | Extended thinking content. Includes `Signature` for verification.                                                            |
| `*TextEvent`       | Assistant text output.                                                                                                      |
| `*ToolUseEvent`    | Tool invocation with name and input.                                                                                        |
| `*ToolResultEvent` | Result from a tool invocation. `Content` is `[]ToolContent` supporting text and image blocks. `Text()` returns concatenated text. |
| `*RateLimitEvent`  | Rate limit status change. Fields: `Status`, `Utilization`, `ResetsAt`, `RateLimitType`, overage fields, `UUID`, `SessionID`, `Raw`. |
| `*StderrEvent`     | A line of stderr output from the CLI process.                                                                               |
| `*ResultEvent`     | Session complete. Text, cost, duration, usage, `StopReason`, `StructuredOutput`, `ModelUsage` (per-model context window, token limits, web search/fetch counts), `ContextSnapshot` (per-API-call usage from last `message_start`/`message_delta`; requires `WithIncludePartialMessages`; nil otherwise). Synthesized if CLI exits cleanly without one. |
| `*ContextManagementEvent` | Emitted when the CLI compresses or summarizes older turns to fit the context window. `Raw` contains the full JSON payload. |
| `*ControlRequestEvent` | Control request from CLI (handled internally in sessions).                                                              |
| `*StreamEvent`     | Partial message update (when `WithIncludePartialMessages` is on).                                                            |
| `*ErrorEvent`      | Error during streaming. `Fatal` field distinguishes process failures (which set `StateFailed`) from non-fatal errors (parse errors, API errors). API errors are classified via `errors.Is` with sentinel errors (see error handling below). |

## Options

| Option                               | Description                                                                                           |
| ------------------------------------ | ----------------------------------------------------------------------------------------------------- |
| `WithBinaryPath(string)`             | Path to the `claude` binary. Only effective in `New()`. Default: `"claude"`.                          |
| `WithModel(Model)`                   | Model to use (`ModelHaiku`, `ModelSonnet`, `ModelOpus`). Default: `ModelSonnet`.                      |
| `WithFallbackModel(Model)`           | Fallback model if primary is unavailable.                                                             |
| `WithBetas(...string)`               | Beta features to enable.                                                                              |
| `WithMaxThinkingTokens(int)`         | Maximum thinking tokens for extended thinking.                                                        |
| `WithSystemPrompt(string)`           | System prompt.                                                                                        |
| `WithSystemPromptFile(string)`       | Load system prompt from a file.                                                                       |
| `WithAppendSystemPrompt(string)`     | Append to the default system prompt.                                                                  |
| `WithAppendSystemPromptFile(string)` | Append to the default system prompt from a file.                                                      |
| `WithTools(...string)`               | Allowed tools. Accepts individual names or comma-separated (`"A,B"` == `"A", "B"`). Deduplicates.     |
| `WithDisallowedTools(...string)`     | Disallowed tools. Same comma/dedup behavior as `WithTools`.                                           |
| `WithBuiltinTools(...string)`        | Restrict available built-in tools. `"default"` for all, `""` for none, or names like `"Bash"`, `"Edit"`. |
| `WithPermissionMode(PermissionMode)` | Permission mode (`PermissionDefault`, `PermissionPlan`, `PermissionAcceptEdits`, `PermissionBypass`, `PermissionDontAsk`, `PermissionAuto`). |
| `WithJSONSchema(string)`             | JSON schema for structured output validation.                                                         |
| `WithMaxBudget(float64)`             | Maximum cost budget in USD.                                                                           |
| `WithMaxTurns(int)`                  | Maximum agentic turns before stopping.                                                                |
| `WithWorkDir(string)`                | Working directory for the CLI process.                                                                |
| `WithAddDirs(...string)`             | Additional directories to allow tool access to.                                                       |
| `WithSessionID(string)`              | Resume a specific session.                                                                            |
| `WithForkSession()`                  | Fork from the session (requires `WithSessionID`).                                                     |
| `WithContinue()`                     | Continue the most recent session.                                                                     |
| `WithEffort(EffortLevel)`            | Effort level (`EffortLow`, `EffortMedium`, `EffortHigh`, `EffortMax`).                                |
| `WithMCPConfig(...string)`           | MCP server configs — file paths or inline JSON strings.                                               |
| `WithStrictMCPConfig()`              | Only use MCP servers from `WithMCPConfig`, ignoring all other MCP configurations.                     |
| `WithAgent(string)`                  | Named agent for the session.                                                                          |
| `WithAgentDef(string)`               | Custom agent definitions as JSON.                                                                     |
| `WithIncludePartialMessages()`       | Include partial message chunks (streaming only).                                                      |
| `WithSettings(string)`               | Path to settings file.                                                                                |
| `WithSettingSources(...string)`      | Setting sources (comma-joined).                                                                       |
| `WithPluginDirs(...string)`          | Plugin directories.                                                                                   |
| `WithResume(string)`                 | Resume a session by ID (mutually exclusive with `WithSessionID`/`WithContinue`).                      |
| `WithCanUseTool(ToolPermissionFunc)` | Tool permission callback (sessions only).                                                             |
| `WithUserInput(UserInputFunc)`       | Dedicated callback for `AskUserQuestion` tool requests (sessions only).                               |
| `WithControlTimeout(time.Duration)` | Timeout for control protocol round-trips (default: 30s). Sessions only.                               |
| `WithInitTimeout(time.Duration)`   | Timeout for the initialize handshake (default: 60s). Increase if MCP servers are slow to connect. Sessions only. |
| `WithPermissionPromptToolName(string)` | Custom permission prompt tool name (default: `"stdio"`). Sessions only.                             |
| `WithEnv(map[string]string)`         | Additional environment variables. Can override `CLAUDE_CODE_ENTRYPOINT` (default: `"sdk-go"`).        |
| `WithExtraArgs(map[string]string)`   | Arbitrary `--key value` flags for forward compatibility. Empty value emits flag only.                  |
| `WithUser(string)`                   | User identifier passed to the CLI.                                                                    |
| `WithStderrCallback(func(string))`   | Called per stderr line in addition to `StderrEvent` emission.                                         |
| `WithFileCheckpointing()`            | Enable SDK file checkpointing via `CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING` env var.                |

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

// Extract retry timing from rate limit errors
var rlErr *claudecli.RateLimitError
if errors.As(err, &rlErr) {
    time.Sleep(rlErr.RetryAfter)
}

// CLI process failure with exit code and stderr
var cliErr *claudecli.Error
if errors.As(err, &cliErr) {
    fmt.Println(cliErr.ExitCode)
    fmt.Println(cliErr.Stderr)
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
  model.go       Model constants
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
  version.go     SDKVersion, MinCLIVersion, CLI version checking with semver parsing
  error.go       Sentinel errors (ErrInvalidRequest, ErrAuth, ErrBilling, ErrPermission, ErrNotFound, ErrRequestTooLarge, ErrRateLimit, ErrAPI, ErrOverloaded), RateLimitError, Error, UnmarshalError
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
