package claudecli

import (
	"slices"
	"testing"
)

// argValue returns the value following flag in args, or "" if not found.
func argValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// argCount returns how many times flag appears in args.
func argCount(args []string, flag string) int {
	n := 0
	for _, a := range args {
		if a == flag {
			n++
		}
	}
	return n
}

func TestBuildArgsDefaults(t *testing.T) {
	args := resolveOptions(nil, nil).buildArgs()

	for _, required := range []string{"--print", "--verbose", "--no-session-persistence"} {
		if !slices.Contains(args, required) {
			t.Errorf("missing %s", required)
		}
	}
	if v, ok := argValue(args, "--output-format"); !ok || v != "stream-json" {
		t.Error("missing stream-json output format")
	}
	if v, ok := argValue(args, "--model"); !ok || v != string(DefaultModel) {
		t.Errorf("missing default model %s", DefaultModel)
	}
}

func TestBuildArgsWithModel(t *testing.T) {
	args := resolveOptions(nil, []Option{WithModel(ModelOpus)}).buildArgs()

	if v, _ := argValue(args, "--model"); v != string(ModelOpus) {
		t.Errorf("expected model %s, got %s", ModelOpus, v)
	}
}

func TestBuildArgsSessionID(t *testing.T) {
	args := resolveOptions(nil, []Option{WithSessionID("test-session")}).buildArgs()

	if v, _ := argValue(args, "--session-id"); v != "test-session" {
		t.Error("missing --session-id")
	}
	if slices.Contains(args, "--no-session-persistence") {
		t.Error("should not have --no-session-persistence with session ID")
	}
}

func TestBuildArgsContinue(t *testing.T) {
	args := resolveOptions(nil, []Option{WithContinue()}).buildArgs()

	if !slices.Contains(args, "--continue") {
		t.Error("missing --continue")
	}
}

func TestBuildArgsTools(t *testing.T) {
	args := resolveOptions(nil, []Option{WithTools("Read", "Write")}).buildArgs()

	if n := argCount(args, "--allowedTools"); n != 2 {
		t.Errorf("expected 2 --allowedTools flags, got %d", n)
	}
}

func TestBuildArgsJSONSchema(t *testing.T) {
	args := resolveOptions(nil, []Option{WithJSONSchema(`{"type":"object"}`)}).buildArgs()

	if v, ok := argValue(args, "--json-schema"); !ok || v != `{"type":"object"}` {
		t.Errorf("missing or wrong --json-schema: %q", v)
	}
}

func TestBuildArgsMaxBudget(t *testing.T) {
	args := resolveOptions(nil, []Option{WithMaxBudget(1.50)}).buildArgs()

	if v, ok := argValue(args, "--max-budget-usd"); !ok || v != "1.50" {
		t.Errorf("missing or wrong --max-budget-usd: %q", v)
	}
}

func TestBuildArgsMaxTurns(t *testing.T) {
	args := resolveOptions(nil, []Option{WithMaxTurns(5)}).buildArgs()

	if v, ok := argValue(args, "--max-turns"); !ok || v != "5" {
		t.Errorf("missing or wrong --max-turns: %q", v)
	}
}

func TestBuildArgsAddDirs(t *testing.T) {
	args := resolveOptions(nil, []Option{WithAddDirs("/tmp", "/var")}).buildArgs()

	if n := argCount(args, "--add-dir"); n != 2 {
		t.Errorf("expected 2 --add-dir flags, got %d", n)
	}
}

func TestBuildArgsSystemPromptFile(t *testing.T) {
	args := resolveOptions(nil, []Option{WithSystemPromptFile("prompt.txt")}).buildArgs()

	if v, ok := argValue(args, "--system-prompt-file"); !ok || v != "prompt.txt" {
		t.Errorf("missing or wrong --system-prompt-file: %q", v)
	}
}

func TestBuildArgsAppendSystemPromptFile(t *testing.T) {
	args := resolveOptions(nil, []Option{WithAppendSystemPromptFile("extra.txt")}).buildArgs()

	if v, ok := argValue(args, "--append-system-prompt-file"); !ok || v != "extra.txt" {
		t.Errorf("missing or wrong --append-system-prompt-file: %q", v)
	}
}

func TestBuildArgsBuiltinTools(t *testing.T) {
	args := resolveOptions(nil, []Option{WithBuiltinTools("Bash", "Edit")}).buildArgs()

	if n := argCount(args, "--tools"); n != 2 {
		t.Errorf("expected 2 --tools flags, got %d", n)
	}
}

func TestBuildArgsAgent(t *testing.T) {
	args := resolveOptions(nil, []Option{WithAgent("reviewer")}).buildArgs()

	if v, ok := argValue(args, "--agent"); !ok || v != "reviewer" {
		t.Errorf("missing or wrong --agent: %q", v)
	}
}

func TestBuildArgsAgentDef(t *testing.T) {
	def := `{"reviewer":{"prompt":"Review code"}}`
	args := resolveOptions(nil, []Option{WithAgentDef(def)}).buildArgs()

	if v, ok := argValue(args, "--agents"); !ok || v != def {
		t.Errorf("missing or wrong --agents: %q", v)
	}
}

func TestBuildArgsIncludePartialMessages(t *testing.T) {
	args := resolveOptions(nil, []Option{WithIncludePartialMessages()}).buildArgs()

	if !slices.Contains(args, "--include-partial-messages") {
		t.Error("missing --include-partial-messages")
	}
}

func TestBuildBlockingArgsUsesJSONFormat(t *testing.T) {
	args := resolveOptions(nil, nil).buildBlockingArgs()

	if v, ok := argValue(args, "--output-format"); !ok || v != "json" {
		t.Errorf("blocking args should use json format, got %q", v)
	}
}

func TestCallOverridesClientDefaults(t *testing.T) {
	opts := resolveOptions(
		[]Option{WithModel(ModelHaiku)},
		[]Option{WithModel(ModelOpus)},
	)
	if opts.model != ModelOpus {
		t.Errorf("expected override model %s, got %s", ModelOpus, opts.model)
	}
}

func TestBuildArgsToolsCommaSeparated(t *testing.T) {
	args := resolveOptions(nil, []Option{WithTools("Read,Write", "Bash")}).buildArgs()

	if n := argCount(args, "--allowedTools"); n != 3 {
		t.Errorf("expected 3 --allowedTools flags, got %d", n)
	}
}

func TestBuildArgsToolsDeduplicates(t *testing.T) {
	args := resolveOptions(nil, []Option{WithTools("Read,Write", "Read")}).buildArgs()

	if n := argCount(args, "--allowedTools"); n != 2 {
		t.Errorf("expected 2 --allowedTools flags (deduped), got %d", n)
	}
}

func TestBuildArgsDisallowedToolsCommaSeparated(t *testing.T) {
	args := resolveOptions(nil, []Option{WithDisallowedTools("Read,Write")}).buildArgs()

	if n := argCount(args, "--disallowedTools"); n != 2 {
		t.Errorf("expected 2 --disallowedTools flags, got %d", n)
	}
}

func TestToolsOverrideReplacesNotMerges(t *testing.T) {
	args := resolveOptions(
		[]Option{WithTools("Read", "Write")},
		[]Option{WithTools("Bash")},
	).buildArgs()

	n := argCount(args, "--allowedTools")
	if n != 1 {
		t.Errorf("expected 1 --allowedTools flag (override replaces), got %d", n)
	}
	if v, _ := argValue(args, "--allowedTools"); v != "Bash" {
		t.Errorf("expected tool 'Bash', got %q", v)
	}
}
