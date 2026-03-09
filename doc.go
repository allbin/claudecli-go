// Package claudecli invokes the Claude Code CLI as a subprocess with typed
// streaming events, functional options, and pluggable execution.
//
// # Prerequisites
//
// The claude CLI binary must be installed and on PATH. See
// https://docs.anthropic.com/en/docs/claude-code for installation.
//
// # Thread Safety
//
// Client is safe for concurrent use — each Run call is independent.
// Stream is single-consumer: only one goroutine should read events.
// Wait() is safe to call concurrently (idempotent, returns cached result).
//
// The package-level Run and RunText functions use a shared default client
// and are safe for concurrent use.
//
// # RunJSON
//
// RunJSON is a package-level generic function rather than a Client method
// because Go does not allow type parameters on methods. Use it as:
//
//	result, info, err := claudecli.RunJSON[MyType](ctx, client, prompt)
//
// RunJSON automatically strips markdown code fences (```json ... ```) before
// unmarshaling. On parse failure it returns *UnmarshalError which contains the
// original model output in RawText for debugging.
package claudecli
