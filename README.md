# claudecli-go

Go package for invoking the [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code) as a subprocess with typed streaming events, functional options, and pluggable execution.

**Requires**: `claude` CLI installed and on PATH.

## Install

```
go get github.com/mdjarv/claudecli-go
```

## Quick start

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/mdjarv/claudecli-go"
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

## Custom executor

The `Executor` interface controls how the CLI process is spawned. Implement it to run Claude in Docker, over SSH, or any other environment.

```go
type Executor interface {
    Start(ctx context.Context, cfg *StartConfig) (*Process, error)
}

type StartConfig struct {
    Args    []string
    Stdin   io.Reader
    Env     map[string]string
    WorkDir string
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
| `*ThinkingEvent`   | Extended thinking content.                                                                                                  |
| `*TextEvent`       | Assistant text output.                                                                                                      |
| `*ToolUseEvent`    | Tool invocation with name and input.                                                                                        |
| `*ToolResultEvent` | Result from a tool invocation.                                                                                              |
| `*RateLimitEvent`  | Rate limit status and utilization.                                                                                          |
| `*StderrEvent`     | A line of stderr output from the CLI process.                                                                               |
| `*ResultEvent`     | Session complete. Accumulated text, cost, duration, token usage.                                                            |
| `*ErrorEvent`      | Error during streaming. `Fatal` field distinguishes process failures (which set `StateFailed`) from non-fatal parse errors. |

## Options

| Option                               | Description                                                                                           |
| ------------------------------------ | ----------------------------------------------------------------------------------------------------- |
| `WithBinaryPath(string)`             | Path to the `claude` binary. Only effective in `New()`. Default: `"claude"`.                          |
| `WithModel(Model)`                   | Model to use (`ModelHaiku`, `ModelSonnet`, `ModelOpus`). Default: `ModelSonnet`.                      |
| `WithFallbackModel(Model)`           | Fallback model if primary is unavailable.                                                             |
| `WithSystemPrompt(string)`           | System prompt.                                                                                        |
| `WithSystemPromptFile(string)`       | Load system prompt from a file.                                                                       |
| `WithAppendSystemPrompt(string)`     | Append to the default system prompt.                                                                  |
| `WithAppendSystemPromptFile(string)` | Append to the default system prompt from a file.                                                      |
| `WithTools(...string)`               | Allowed tools — execute without permission prompts (repeatable).                                      |
| `WithDisallowedTools(...string)`     | Disallowed tools — removed from model context (repeatable).                                           |
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
| `WithEffort(string)`                 | Effort level (`"low"`, `"medium"`, `"high"`).                                                         |
| `WithMCPConfig(...string)`           | MCP server configs — file paths or inline JSON strings.                                               |
| `WithStrictMCPConfig()`              | Only use MCP servers from `WithMCPConfig`, ignoring all other MCP configurations.                     |
| `WithAgent(string)`                  | Named agent for the session.                                                                          |
| `WithAgentDef(string)`               | Custom agent definitions as JSON.                                                                     |
| `WithIncludePartialMessages()`       | Include partial message chunks (streaming only).                                                      |
| `WithEnv(map[string]string)`         | Additional environment variables.                                                                     |

Options set at call time **replace** (not merge with) client-level defaults.

## Error handling

```go
import "errors"

text, _, err := client.RunText(ctx, prompt)

// Empty output (no text events received)
if errors.Is(err, claudecli.ErrEmptyOutput) { ... }

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
  executor.go    Executor interface, LocalExecutor, FixtureExecutor
  parse.go       JSONL stream parser (decoupled from process lifecycle)
  stream.go      Stream with State(), Events(), Next(), Wait(), Close()
  client.go      Client struct, Run/RunText/RunJSON, package-level shortcuts
  blocking.go    RunBlocking/RunBlockingJSON — non-streaming JSON output mode
  error.go       Typed Error (ExitCode, Stderr, Message), UnmarshalError (RawText)
```

**Layers:**

1. **Parse** (`parse.go`) — JSONL deserialization into typed events. Zero coupling to process execution. Testable with fixtures. Returns immediately after the result event to avoid blocking on CLI hang bugs.
2. **Execute** (`executor.go`) — `Executor` interface abstracts process spawning. `LocalExecutor` handles the real CLI with platform-aware line buffering (`stdbuf -oL` on Linux).
3. **Client** (`client.go`) — Composes executor + options. Builds CLI args, manages goroutines, emits events through the Stream.
4. **Blocking** (`blocking.go`) — Non-streaming path using `--output-format json`. Simpler execution model for `RunBlocking`/`RunBlockingJSON`.

## Known limitations / TODO

- **JSONL format is unversioned** — Claude CLI's `stream-json` output format is not formally versioned by Anthropic. Tested with Claude Code CLI 2.x. Breaking changes across CLI versions are possible.
- **No retry/backoff** — `RateLimitEvent` is emitted but the package does not automatically retry or backoff. Consumers must implement their own retry logic.
- **`stdbuf` recommended on Linux** — `LocalExecutor` uses `stdbuf -oL` for line-buffered stdout on Linux when available, falling back to direct execution without it.
