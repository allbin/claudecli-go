# Upstream Sync Process

How to keep claudecli-go up to date with `claude-agent-sdk-python` (the canonical Python SDK).

## Why the Python SDK

The official Python SDK (`anthropics/claude-agent-sdk-python`) is the most active reference implementation. It tracks the same CLI JSONL protocol we do, and changes there often signal:

- Bug fixes for real-world issues (shutdown races, env var precedence)
- New CLI features we should support (new event types, new fields)
- Reverts of bad ideas (useful to avoid porting something that got rolled back)

The TypeScript SDK (`@anthropic-ai/claude-agent-sdk` inside `claude-cli-internal`) is harder to access but can also be checked when the Python SDK references TS PRs.

## Process

### 1. Fetch and review recent commits

```bash
cd /path/to/claude-agent-sdk-python
git fetch origin
git log --oneline HEAD..origin/main
```

Skip: CLI version bumps (`chore: bump bundled CLI version`), CI-only changes, docs-only changes.

### 2. Triage each commit

For each interesting commit, check the diff (`git show <sha> -- '*.py'`) and classify:

| Category | Action |
|----------|--------|
| **Bug fix** in shared protocol/lifecycle code | Port to Go — highest priority |
| **New event type or field** the CLI emits | Add to event.go + parse.go |
| **New API surface** (session mutations, session info) | Evaluate if needed, often skip |
| **Revert** | Check if we had the reverted behavior — remove if so |
| **Python-specific** (packaging, typing, CI) | Skip |

### 3. Port changes

- Match the fix semantics, not the Python code structure
- Update `rawEvent`/`rawFoo` structs in `parse.go` for new JSON fields
- Update event types in `event.go`
- Update both `ParseEvents()` and `Session.readLoop()` if they handle the same event type
- Add tests
- Update README.md (event table, options table, known limitations)

### 4. Verify

```bash
go build ./...
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
```

## Sync log

### 2026-03-25: Synced with v0.1.49..v0.1.50

Commits reviewed: 24 (12 CLI bumps/CI skipped, 12 substantive)

Ported:
- `40cc6f5` fix: graceful subprocess shutdown — wait 5s after stdin EOF before SIGTERM
- `6d77aef` fix: CLAUDE_CODE_ENTRYPOINT default-if-absent — user env overrides win
- `2d5c3cb` feat: typed RateLimitEvent — added ResetsAt, RateLimitType, overage fields, UUID, SessionID, Raw map

Evaluated and skipped:
- `fc82420` per-turn usage on AssistantMessage — our event model differs (no AssistantMessage type)
- `fad1b84` AgentDefinition skills/memory/mcpServers — we use opaque JSON (WithAgentDef)
- `a6e4e58` rename_session — file-level session mutation, not core streaming
- `2513c45` tag_session — file-level session mutation, not core streaming
- `f144dcc` get_session_info + SDKSessionInfo fields — not core streaming
- `21560e3` revert fine-grained tool streaming — we never had this behavior
