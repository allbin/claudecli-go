package claudecli

import (
	"slices"
	"testing"
)

func TestBuildArgsDefaults(t *testing.T) {
	opts := resolveOptions(nil, nil)
	args := opts.buildArgs()

	if !slices.Contains(args, "--print") {
		t.Error("missing --print")
	}
	if !slices.Contains(args, "--verbose") {
		t.Error("missing --verbose")
	}
	if !slices.Contains(args, "stream-json") {
		t.Error("missing stream-json output format")
	}
	if !slices.Contains(args, string(DefaultModel)) {
		t.Errorf("missing default model %s", DefaultModel)
	}
	if !slices.Contains(args, "--no-session-persistence") {
		t.Error("missing --no-session-persistence for default (no session)")
	}
}

func TestBuildArgsWithModel(t *testing.T) {
	opts := resolveOptions(nil, []Option{WithModel(ModelOpus)})
	args := opts.buildArgs()

	if !slices.Contains(args, string(ModelOpus)) {
		t.Error("model not set")
	}
}

func TestBuildArgsSessionID(t *testing.T) {
	opts := resolveOptions(nil, []Option{WithSessionID("test-session")})
	args := opts.buildArgs()

	if !slices.Contains(args, "--session-id") {
		t.Error("missing --session-id")
	}
	if !slices.Contains(args, "test-session") {
		t.Error("missing session ID value")
	}
	if slices.Contains(args, "--no-session-persistence") {
		t.Error("should not have --no-session-persistence with session ID")
	}
}

func TestBuildArgsContinue(t *testing.T) {
	opts := resolveOptions(nil, []Option{WithContinue()})
	args := opts.buildArgs()

	if !slices.Contains(args, "--continue") {
		t.Error("missing --continue")
	}
}

func TestBuildArgsTools(t *testing.T) {
	opts := resolveOptions(nil, []Option{WithTools("Read", "Write")})
	args := opts.buildArgs()

	count := 0
	for _, a := range args {
		if a == "--allowedTools" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("expected 2 --allowedTools flags, got %d", count)
	}
}

func TestCallOverridesClientDefaults(t *testing.T) {
	defaults := []Option{WithModel(ModelHaiku)}
	overrides := []Option{WithModel(ModelOpus)}
	opts := resolveOptions(defaults, overrides)

	if opts.model != ModelOpus {
		t.Errorf("expected override model %s, got %s", ModelOpus, opts.model)
	}
}

func TestBuildArgsJSONSchema(t *testing.T) {
	opts := resolveOptions(nil, []Option{WithJSONSchema(`{"type":"object"}`)})
	args := opts.buildArgs()

	for i, a := range args {
		if a == "--json-schema" {
			if args[i+1] != `{"type":"object"}` {
				t.Errorf("unexpected schema value: %q", args[i+1])
			}
			return
		}
	}
	t.Error("missing --json-schema flag")
}

func TestBuildArgsMaxBudget(t *testing.T) {
	opts := resolveOptions(nil, []Option{WithMaxBudget(1.50)})
	args := opts.buildArgs()

	for i, a := range args {
		if a == "--max-budget-usd" {
			if args[i+1] != "1.50" {
				t.Errorf("unexpected budget value: %q", args[i+1])
			}
			return
		}
	}
	t.Error("missing --max-budget-usd flag")
}

func TestToolsOverrideReplacesNotMerges(t *testing.T) {
	defaults := []Option{WithTools("Read", "Write")}
	overrides := []Option{WithTools("Bash")}
	opts := resolveOptions(defaults, overrides)
	args := opts.buildArgs()

	count := 0
	for i, a := range args {
		if a == "--allowedTools" {
			count++
			if args[i+1] != "Bash" {
				t.Errorf("expected tool 'Bash', got %q", args[i+1])
			}
		}
	}
	if count != 1 {
		t.Errorf("expected 1 --allowedTools flag (override replaces), got %d", count)
	}
}
