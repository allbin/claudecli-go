package claudecli

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
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

func TestBuildArgsBetas(t *testing.T) {
	args := resolveOptions(nil, []Option{WithBetas("interleaved-thinking", "extended-output")}).buildArgs()

	if v, ok := argValue(args, "--betas"); !ok || v != "interleaved-thinking,extended-output" {
		t.Errorf("missing or wrong --betas: %q", v)
	}
}

func TestBuildArgsMaxThinkingTokens(t *testing.T) {
	args := resolveOptions(nil, []Option{WithMaxThinkingTokens(4096)}).buildArgs()

	if v, ok := argValue(args, "--max-thinking-tokens"); !ok || v != "4096" {
		t.Errorf("missing or wrong --max-thinking-tokens: %q", v)
	}
}

func TestBuildArgsSettings(t *testing.T) {
	args := resolveOptions(nil, []Option{WithSettings("/tmp/settings.json")}).buildArgs()

	if v, ok := argValue(args, "--settings"); !ok || v != "/tmp/settings.json" {
		t.Errorf("missing or wrong --settings: %q", v)
	}
}

func TestBuildArgsSettingSources(t *testing.T) {
	args := resolveOptions(nil, []Option{WithSettingSources("user", "project")}).buildArgs()

	if v, ok := argValue(args, "--setting-sources"); !ok || v != "user,project" {
		t.Errorf("missing or wrong --setting-sources: %q", v)
	}
}

func TestBuildArgsPluginDirs(t *testing.T) {
	args := resolveOptions(nil, []Option{WithPluginDirs("/tmp/plugins", "/opt/plugins")}).buildArgs()

	if n := argCount(args, "--plugin-dir"); n != 2 {
		t.Errorf("expected 2 --plugin-dir flags, got %d", n)
	}
}

func TestBuildSessionArgs(t *testing.T) {
	opts := resolveOptions(nil, []Option{
		WithModel(ModelOpus),
		WithSessionID("sess-123"),
	})
	args := opts.buildSessionArgs()

	if v, ok := argValue(args, "--input-format"); !ok || v != "stream-json" {
		t.Error("missing --input-format stream-json")
	}
	for _, a := range args {
		if a == "--print" {
			t.Error("session args should not have --print")
		}
		if a == "--no-session-persistence" {
			t.Error("session args should not have --no-session-persistence")
		}
	}
}

func TestBuildArgsResume(t *testing.T) {
	args := resolveOptions(nil, []Option{WithResume("sess-abc")}).buildArgs()

	if v, ok := argValue(args, "--resume"); !ok || v != "sess-abc" {
		t.Errorf("missing or wrong --resume: %q", v)
	}
	if slices.Contains(args, "--no-session-persistence") {
		t.Error("should not have --no-session-persistence with resume")
	}
}

func TestBuildArgsBare(t *testing.T) {
	args := resolveOptions(nil, []Option{WithBare()}).buildArgs()
	if !slices.Contains(args, "--bare") {
		t.Error("missing --bare")
	}
}

func TestBuildArgsBareInAllBuilders(t *testing.T) {
	opts := resolveOptions(nil, []Option{WithBare()})
	for name, args := range map[string][]string{
		"buildArgs":        opts.buildArgs(),
		"buildBlockingArgs": opts.buildBlockingArgs(),
		"buildSessionArgs": opts.buildSessionArgs(),
	} {
		if !slices.Contains(args, "--bare") {
			t.Errorf("--bare missing from %s", name)
		}
	}
}

func TestBuildArgsDangerouslySkipPermissions(t *testing.T) {
	args := resolveOptions(nil, []Option{WithDangerouslySkipPermissions()}).buildArgs()
	if !slices.Contains(args, "--allow-dangerously-skip-permissions") {
		t.Error("missing --allow-dangerously-skip-permissions")
	}
	if !slices.Contains(args, "--dangerously-skip-permissions") {
		t.Error("missing --dangerously-skip-permissions")
	}
}

func TestBuildArgsSessionName(t *testing.T) {
	args := resolveOptions(nil, []Option{WithSessionName("my-task")}).buildArgs()
	if v, ok := argValue(args, "--name"); !ok || v != "my-task" {
		t.Errorf("missing or wrong --name: %q", v)
	}
}

func TestBuildSessionArgsSessionName(t *testing.T) {
	opts := resolveOptions(nil, []Option{
		WithSessionName("my-task"),
		WithSessionID("sess-123"),
	})
	args := opts.buildSessionArgs()
	if v, ok := argValue(args, "--name"); !ok || v != "my-task" {
		t.Errorf("missing or wrong --name in session args: %q", v)
	}
}

func TestBuildArgsDebugFile(t *testing.T) {
	args := resolveOptions(nil, []Option{WithDebugFile("/tmp/debug.log")}).buildArgs()
	if v, ok := argValue(args, "--debug-file"); !ok || v != "/tmp/debug.log" {
		t.Errorf("missing or wrong --debug-file: %q", v)
	}
}

func TestBuildArgsDisableSlashCommands(t *testing.T) {
	args := resolveOptions(nil, []Option{WithDisableSlashCommands()}).buildArgs()
	if !slices.Contains(args, "--disable-slash-commands") {
		t.Error("missing --disable-slash-commands")
	}
}

func TestBuildArgsExtraArgs(t *testing.T) {
	args := resolveOptions(nil, []Option{WithExtraArgs(map[string]string{
		"custom-flag": "value1",
		"bool-flag":   "",
	})}).buildArgs()

	if v, ok := argValue(args, "--custom-flag"); !ok || v != "value1" {
		t.Errorf("missing or wrong --custom-flag: %q", v)
	}
	if !slices.Contains(args, "--bool-flag") {
		t.Error("missing --bool-flag")
	}
}

func TestBuildArgsUser(t *testing.T) {
	args := resolveOptions(nil, []Option{WithUser("user-123")}).buildArgs()

	if v, ok := argValue(args, "--user"); !ok || v != "user-123" {
		t.Errorf("missing or wrong --user: %q", v)
	}
}

func TestBuildArgsPermissionPromptToolName(t *testing.T) {
	opts := resolveOptions(nil, []Option{
		WithCanUseTool(func(name string, input json.RawMessage) (*PermissionResponse, error) {
			return &PermissionResponse{Allow: true}, nil
		}),
		WithPermissionPromptToolName("custom-tool"),
	})
	args := opts.buildSessionArgs()

	if v, ok := argValue(args, "--permission-prompt-tool"); !ok || v != "custom-tool" {
		t.Errorf("missing or wrong --permission-prompt-tool: %q", v)
	}
}

func TestBuildArgsTimeoutNotEmitted(t *testing.T) {
	args := resolveOptions(nil, []Option{WithTimeout(90 * time.Second)}).buildArgs()

	if slices.Contains(args, "--timeout") {
		t.Error("--timeout should not be emitted as CLI arg (not a valid Claude CLI flag)")
	}
}

func TestBuildArgsEffort(t *testing.T) {
	args := resolveOptions(nil, []Option{WithEffort("high")}).buildArgs()

	if v, ok := argValue(args, "--effort"); !ok || v != "high" {
		t.Errorf("missing or wrong --effort: %q", v)
	}
}

// Fix #12: EffortLevel typed constants work with WithEffort.
func TestBuildArgsEffortTypedConstants(t *testing.T) {
	for _, level := range []EffortLevel{EffortLow, EffortMedium, EffortHigh, EffortMax} {
		args := resolveOptions(nil, []Option{WithEffort(level)}).buildArgs()
		v, ok := argValue(args, "--effort")
		if !ok {
			t.Errorf("missing --effort for level %q", level)
			continue
		}
		if v != string(level) {
			t.Errorf("--effort = %q, want %q", v, level)
		}
	}
}

func TestBuildArgsReplayUserMessages(t *testing.T) {
	opts := resolveOptions(nil, []Option{WithReplayUserMessages()})
	// Should appear in session args
	sargs := opts.buildSessionArgs()
	if !slices.Contains(sargs, "--replay-user-messages") {
		t.Error("buildSessionArgs missing --replay-user-messages")
	}
	// Should NOT appear in print-mode args
	args := opts.buildArgs()
	if slices.Contains(args, "--replay-user-messages") {
		t.Error("buildArgs should not have --replay-user-messages")
	}
	bargs := opts.buildBlockingArgs()
	if slices.Contains(bargs, "--replay-user-messages") {
		t.Error("buildBlockingArgs should not have --replay-user-messages")
	}
}

// Fix #14: extraArgs produce deterministic (sorted) order.
func TestBuildArgsExtraArgsSorted(t *testing.T) {
	extra := map[string]string{
		"zzz-flag": "z",
		"aaa-flag": "a",
		"mmm-flag": "m",
	}
	// Build args twice and verify identical order.
	args1 := resolveOptions(nil, []Option{WithExtraArgs(extra)}).buildArgs()
	args2 := resolveOptions(nil, []Option{WithExtraArgs(extra)}).buildArgs()

	// Extract just the extra arg flags
	var flags1, flags2 []string
	for _, a := range args1 {
		if a == "--aaa-flag" || a == "--mmm-flag" || a == "--zzz-flag" {
			flags1 = append(flags1, a)
		}
	}
	for _, a := range args2 {
		if a == "--aaa-flag" || a == "--mmm-flag" || a == "--zzz-flag" {
			flags2 = append(flags2, a)
		}
	}

	if len(flags1) != 3 {
		t.Fatalf("expected 3 extra flags, got %d", len(flags1))
	}
	// Verify sorted order
	if flags1[0] != "--aaa-flag" || flags1[1] != "--mmm-flag" || flags1[2] != "--zzz-flag" {
		t.Errorf("extra args not sorted: %v", flags1)
	}
	// Verify deterministic
	for i := range flags1 {
		if flags1[i] != flags2[i] {
			t.Errorf("non-deterministic order: %v vs %v", flags1, flags2)
			break
		}
	}
}

func TestResolveCanUseTool(t *testing.T) {
	fn := func(name string, input json.RawMessage) (*PermissionResponse, error) {
		return &PermissionResponse{Allow: true}, nil
	}
	got := ResolveCanUseTool(WithCanUseTool(fn))
	if got == nil {
		t.Fatal("expected non-nil callback")
	}

	got2 := ResolveCanUseTool(WithModel(ModelSonnet))
	if got2 != nil {
		t.Fatal("expected nil callback without WithCanUseTool")
	}
}

func TestBuildSessionArgsWithCanUseTool(t *testing.T) {
	opts := resolveOptions(nil, []Option{
		WithCanUseTool(func(name string, input json.RawMessage) (*PermissionResponse, error) {
			return &PermissionResponse{Allow: true}, nil
		}),
	})
	args := opts.buildSessionArgs()

	var hasPermTool bool
	for i, a := range args {
		if a == "--permission-prompt-tool" && i+1 < len(args) && args[i+1] == "stdio" {
			hasPermTool = true
		}
	}
	if !hasPermTool {
		t.Error("missing --permission-prompt-tool stdio")
	}
}

func TestBuildCommonArgsIncludesSharedFlags(t *testing.T) {
	opts := resolveOptions(nil, []Option{
		WithModel(ModelOpus),
		WithTools("Read", "Write"),
		WithSystemPrompt("test prompt"),
		WithMaxBudget(2.0),
		WithAddDirs("/tmp"),
		WithMCPConfig("/mcp.json"),
		WithAgent("reviewer"),
		WithSettings("/settings.json"),
		WithEffort("high"),
	})
	args := opts.buildCommonArgs()

	checks := map[string]string{
		"--model":          string(ModelOpus),
		"--system-prompt":  "test prompt",
		"--max-budget-usd": "2.00",
		"--mcp-config":     "/mcp.json",
		"--agent":          "reviewer",
		"--settings":       "/settings.json",
		"--effort":         "high",
	}
	for flag, want := range checks {
		if v, ok := argValue(args, flag); !ok || v != want {
			t.Errorf("buildCommonArgs missing %s=%s, got %q", flag, want, v)
		}
	}
	if n := argCount(args, "--allowedTools"); n != 2 {
		t.Errorf("expected 2 --allowedTools, got %d", n)
	}
	if n := argCount(args, "--add-dir"); n != 1 {
		t.Errorf("expected 1 --add-dir, got %d", n)
	}
}

func TestBuildCommonArgsExcludesModeSpecificFlags(t *testing.T) {
	opts := resolveOptions(nil, []Option{WithModel(ModelOpus)})
	args := opts.buildCommonArgs()

	forbidden := []string{
		"--print",
		"--verbose",
		"--output-format",
		"--input-format",
		"--no-session-persistence",
		"--permission-prompt-tool",
	}
	for _, flag := range forbidden {
		if slices.Contains(args, flag) {
			t.Errorf("buildCommonArgs should not contain %s", flag)
		}
	}
}

func TestBuildersOutputUnchangedRegression(t *testing.T) {
	// Verify all three builders produce the same args as before the refactor
	// for a representative set of options.
	opts := resolveOptions(nil, []Option{
		WithModel(ModelOpus),
		WithTools("Read"),
		WithSystemPrompt("sp"),
		WithMaxBudget(1.0),
		WithEffort("high"),
	})

	// buildArgs: must have --print, --verbose, --output-format stream-json, --no-session-persistence
	args := opts.buildArgs()
	for _, required := range []string{"--print", "--verbose", "--no-session-persistence"} {
		if !slices.Contains(args, required) {
			t.Errorf("buildArgs missing %s", required)
		}
	}
	if v, _ := argValue(args, "--output-format"); v != "stream-json" {
		t.Errorf("buildArgs output-format = %q, want stream-json", v)
	}

	// buildBlockingArgs: must have --print, --verbose, --output-format json
	bargs := opts.buildBlockingArgs()
	for _, required := range []string{"--print", "--verbose"} {
		if !slices.Contains(bargs, required) {
			t.Errorf("buildBlockingArgs missing %s", required)
		}
	}
	if v, _ := argValue(bargs, "--output-format"); v != "json" {
		t.Errorf("buildBlockingArgs output-format = %q, want json", v)
	}

	// buildSessionArgs: must have --verbose, --input-format stream-json, NO --print
	sargs := opts.buildSessionArgs()
	if !slices.Contains(sargs, "--verbose") {
		t.Error("buildSessionArgs missing --verbose")
	}
	if v, _ := argValue(sargs, "--input-format"); v != "stream-json" {
		t.Errorf("buildSessionArgs input-format = %q, want stream-json", v)
	}
	if slices.Contains(sargs, "--print") {
		t.Error("buildSessionArgs should not have --print")
	}
	if slices.Contains(sargs, "--no-session-persistence") {
		t.Error("buildSessionArgs should not have --no-session-persistence")
	}

	// All three should share the common flags
	for _, a := range [][]string{args, bargs, sargs} {
		if v, _ := argValue(a, "--model"); v != string(ModelOpus) {
			t.Errorf("missing model in builder output")
		}
		if v, _ := argValue(a, "--effort"); v != "high" {
			t.Errorf("missing effort in builder output")
		}
		if n := argCount(a, "--allowedTools"); n != 1 {
			t.Errorf("expected 1 --allowedTools, got %d", n)
		}
	}
}
