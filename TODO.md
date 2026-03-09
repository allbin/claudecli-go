# TODO

Items to fix, improve, or investigate before calling this package production-ready.

## Bugs

- [x] **Indentation in client.go goroutine** — fixed: rewrote client.go cleanly.
- [x] **`CLAUDECLI_WORKDIR` is a magic env key** — fixed: introduced `StartConfig` struct with first-class `WorkDir` field. No more env smuggling.
- [x] **`FixtureExecutorFromFile` leaks file handle** — fixed: uses `os.ReadFile` + `bytes.NewReader` instead of holding open file.
- [x] **`buildEnv` doesn't deduplicate** — fixed: skips parent env keys that exist in overrides.
- [x] **`stderrLines` race condition** — fixed: added recover in stderr goroutine to ensure `stderrDone` always closes.
- [x] **`Close()` doesn't cancel the context** — fixed: `Client.Run` wraps context with cancel, `Close()` calls cancel before draining.
- [x] **`ErrorEvent` sets `StateFailed` on any error** — fixed: added `Fatal` field to `ErrorEvent`. Only fatal errors (process failures) transition to `StateFailed`. Parse errors are non-fatal by default.

## API design

- [x] **`WorkDir` should be a first-class executor parameter** — fixed: `StartConfig` struct bundles args, stdin, env, and workdir.
- [x] **`RunJSON` is a package function, not a method** — documented in `doc.go`: Go generics limitation prevents type params on methods.
- [x] **No way to set `BinaryPath` via options** — fixed: `WithBinaryPath` option on `New()`.
- [x] **`State` has no String() method** — fixed: added manual `String()` implementation.
- [x] **`Event` interface has no String() method** — fixed: added `fmt.Stringer` to all event types.
- [x] **Consider `State` change callbacks** — decided: event-based consumption is sufficient. Not needed.
- [x] **Package-level `Run`/`RunText` use a shared `defaultClient`** — documented thread safety in `doc.go`.

## Testing

- [x] **No integration test with real CLI** — fixed: build-tagged (`//go:build integration`) tests for RunText and streaming.
- [x] **No test for `Client.Run` goroutine lifecycle** — fixed: added tests for start failure, context cancellation, and full event flow via fixtures.
- [x] **No test for `FixtureExecutor` via `Client`** — fixed: `TestClientRunWithFixture` and `TestClientRunTextWithFixture` exercise the full `Client.Run` -> `Stream` path.
- [x] **No test for option override semantics** — fixed: `TestToolsOverrideReplacesNotMerges` verifies call-level tools replace client defaults.
- [x] **No test for `Close()` behavior** — fixed: `TestClientRunClose` verifies Close drains and completes.
- [x] **No test for concurrent `Wait()` calls** — fixed: `TestStreamConcurrentWait` runs 10 goroutines calling Wait concurrently.
- [x] **No test for malformed JSONL** — fixed: `TestParseMalformedJSONL` verifies error events emitted and parsing continues.
- [x] **No benchmarks** — fixed: `BenchmarkParseBasicStream` and `BenchmarkParseToolUseStream` added.

## Robustness

- [x] **Validate `claude` binary exists before first Run** — fixed: `exec.LookPath` check with clear error message.
- [x] **`stdbuf` fallback on Linux** — fixed: falls back to running `claude` directly if `stdbuf` is not on PATH.
- [x] **Scanner buffer size may be insufficient** — fixed: increased max buffer from 1MB to 10MB.
- [x] **Context cancellation during `newStream` interpose goroutine** — fixed: interpose goroutine selects on `ctx.Done()` to avoid blocking forever.
- [x] **Process cleanup on context cancel** — fixed: sends SIGTERM with 5s grace period before SIGKILL (non-Windows).

## Documentation

- [x] **Add `example_test.go`** — fixed: runnable examples for fixture-based RunText and streaming.
- [x] **Add package-level godoc comment** — fixed: `doc.go` with package overview, prerequisites, thread safety, and RunJSON rationale.
- [x] **Document JSONL format version compatibility** — fixed: noted in README known limitations.
- [x] **Document thread safety** — fixed: documented in `doc.go`.
- [ ] **Add CHANGELOG.md** — track changes from the start.
- [x] **Add LICENSE file** — fixed: MIT license added.

## Future considerations

- [x] **Investigate `--output-format json` mode** — fixed: `RunBlocking`/`RunBlockingJSON` in `blocking.go`. Uses `--output-format json` for simpler non-streaming path with `structured_output` support.
- [ ] **Multi-turn sessions** — current API is one-shot (prompt in, events out). Multi-turn would need a higher-level abstraction that manages session IDs across calls.
- [ ] **Event filtering/middleware** — allow consumers to register transforms or filters on the event stream.
- [ ] **Structured error parsing** — Claude CLI sometimes returns structured JSON error bodies. Parse these into typed error variants.
- [ ] **CI pipeline** — set up GitHub Actions for `go vet`, `go test`, and linting on push.
- [ ] **`--timeout` option** — CLI has a built-in timeout flag. Evaluate whether to expose it or rely on context deadlines only.
