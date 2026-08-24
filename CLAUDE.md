# Commands

- Build: `go build ./...`
- Test: `go test ./... -count=1`
- Test with race detector: `go test -race ./... -count=1`
- Vet: `go vet ./...`
- Benchmarks: `go test -bench=. -benchmem ./...`
- Integration tests (requires real CLI + API key): `go test -tags=integration ./... -count=1`

# Investigating CLI behavior

The CLI has no formal spec. When investigating what events/fields the CLI actually emits, use `cmd/capture` to record raw traffic:

```bash
# Capture raw JSONL from a live CLI session:
go run ./cmd/capture -prompt "your prompt here" -out tmp

# Replay and analyze a captured file:
go run ./cmd/capture -analyze tmp/raw-stdout.jsonl
```

Output: `tmp/raw-stdout.jsonl` (raw JSONL), `tmp/raw-stderr.log` (stderr). The tool also pipes through `ParseEvents` and prints a summary of event types seen, highlighting any `UnknownEvent` instances.

Flags: `-prompt` (default triggers Agent tool), `-out` (output dir, default `tmp`), `-timeout` (default 2m), `-analyze` (replay mode).

## Reading the CLI's own behavior out of the binary

For behavior that never reaches the event stream — install layouts, config keys, update endpoints — the native binary is a bundled JS payload and greps cleanly. Do not `grep` the 300 MB file directly (it is slow and hits regex complexity limits); dump strings once and search that:

```bash
strings -n 6 ~/.local/share/claude/versions/<v> > /tmp/native-strings.txt
```

It contains the settings zod schema (key names, enums, defaults), env-var names, URL constants, and the CLI's own bundled docs — which state rules in prose, e.g. how the update check resolves a channel per install method. Treat it as the authority when the docs are silent, and verify the endpoints it names with `curl` before relying on them.

# Gotchas

- Verify CLI flags exist (`claude --help | grep`) before adding new options to `buildArgs()` — the CLI has no formal spec and flags change between versions.
- Update README.md (options table, architecture, known limitations) before considering a task complete.
- Keep `CHANGELOG.md` up to date as you make changes: add user-facing changes under `## [Unreleased]` (Added/Changed/Fixed, with upgrade notes for behavior changes) in the same PR. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). On release, rename `[Unreleased]` to the version with a date and add a fresh empty `[Unreleased]` on top.
