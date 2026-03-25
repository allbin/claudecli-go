# Improvements from t3code SDK Comparison

Findings from comparing claudecli-go against t3code's use of `@anthropic-ai/claude-agent-sdk` (the official TypeScript SDK for Claude Code). Both libraries wrap the same CLI with the same JSONL protocol — t3code just uses Anthropic's first-party implementation.

## Gaps

### Fast Mode Flag

t3code passes `fastMode: true` as a query option. The CLI accepts this. We don't expose it.

**Fix:** Add `WithFastMode()` option that passes the appropriate flag.

### SDK Version Tracking

We hardcode `CLAUDE_AGENT_SDK_VERSION=0.2.0`. t3code's SDK reports its actual npm version. If the CLI ever gates behavior on this value, we'll be stuck on stale behavior.

**Fix:** Expose a package-level `Version` constant, bump it with releases, and use it for the env var.

### Scanner Buffer vs Large Tool Outputs

We cap at 256KB per line, 10MB total. The TypeScript SDK uses Node.js streams with no fixed line-length limit. Very large tool outputs (e.g., `Read` on a big file, massive `git diff`) could exceed our per-line buffer.

**Fix:** Evaluate whether the 256KB per-line limit has caused real failures. If so, increase it or switch to a streaming JSON decoder that doesn't require full-line buffering.

### Structured Error Classification

We parse stderr JSON into `ErrorDetails{Type, Message, RetryAfter}` and have `IsRateLimit()`, `IsAuth()`, `IsOverloaded()` helpers. Good. But the `ErrorEvent` emitted on the stream doesn't carry this structure — it's just an `error` interface.

**Fix:** Consider enriching `ErrorEvent` with optional `Details *ErrorDetails` so consumers can classify errors from the event stream without waiting for `Wait()`.

## Already Covered (Parity Confirmed)

These features exist in the TypeScript SDK and we already support them:

- Control protocol (`control_request`/`control_response`) for bidirectional sessions
- Permission callback (`canUseTool`) with `allow`/`deny`/`updatedInput`
- User input callback (AskUserQuestion routing)
- Resume via session ID
- Multimodal content blocks (image, document)
- MCP config, reconnect, toggle, status
- File checkpointing and rewind
- Effort levels
- Max thinking tokens
- Model/permission mode switching mid-session
- Task cancellation (`StopTask`)
- `--include-partial-messages` / `StreamEvent`
- Permission modes (default, plan, acceptEdits, bypass, dontAsk, auto)
- Graceful shutdown (SIGTERM + grace period)

## Architectural Notes

t3code's SDK wraps identical CLI flags:
- `--print --verbose --output-format stream-json` for streaming
- `--input-format stream-json --permission-prompt-tool stdio` for interactive sessions
- Same `control_request`/`control_response` JSON shape
- Same event types: system, assistant, result, stream_event, rate_limit_event

The protocol is the same. Our implementation is clean and matches the contract well. The gaps above are minor — the library is in good shape.
