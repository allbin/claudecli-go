package claudecli

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// The CLI accepts --include-hook-events, --forward-subagent-text and
// --prompt-suggestions only when the output format is stream-json. In
// blocking mode (--output-format json) it rejects them outright, so they must
// never reach that path.
func TestStreamOnlyFlagsOmittedInBlockingMode(t *testing.T) {
	opts := []Option{
		WithIncludeHookEvents(),
		WithForwardSubagentText(),
		WithPromptSuggestions(),
	}
	streamOnly := []string{
		"--include-hook-events",
		"--forward-subagent-text",
		"--prompt-suggestions",
	}

	blocking := resolveOptions(nil, opts).buildBlockingArgs()
	for _, f := range streamOnly {
		if slices.Contains(blocking, f) {
			t.Errorf("blocking args must not contain %s: %v", f, blocking)
		}
	}

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"stream", resolveOptions(nil, opts).buildArgs()},
		{"session", resolveOptions(nil, opts).buildSessionArgs()},
	} {
		for _, f := range streamOnly {
			if !slices.Contains(tc.args, f) {
				t.Errorf("%s args missing %s: %v", tc.name, f, tc.args)
			}
		}
	}
}

// --prompt-suggestions takes an optional value. Emitted bare it would consume
// whatever follows it, so the SDK always passes the value explicitly.
func TestPromptSuggestionsPassesExplicitValue(t *testing.T) {
	args := resolveOptions(nil, []Option{WithPromptSuggestions()}).buildArgs()
	v, ok := argValue(args, "--prompt-suggestions")
	if !ok || v != "true" {
		t.Errorf("expected --prompt-suggestions true, got %q (ok=%v)", v, ok)
	}
}

func TestBuildArgsNewCLIOptions(t *testing.T) {
	args := resolveOptions(nil, []Option{
		WithSafeMode(),
		WithAutoCompact("200000"),
		WithExcludeDynamicSystemPromptSections(),
		WithPluginURLs("https://example.com/a.zip", "https://example.com/b.zip"),
	}).buildArgs()

	if !slices.Contains(args, "--safe-mode") {
		t.Error("missing --safe-mode")
	}
	if v, ok := argValue(args, "--autocompact"); !ok || v != "200000" {
		t.Errorf("missing or wrong --autocompact: %q", v)
	}
	if !slices.Contains(args, "--exclude-dynamic-system-prompt-sections") {
		t.Error("missing --exclude-dynamic-system-prompt-sections")
	}
	var urls []string
	for i, a := range args {
		if a == "--plugin-url" && i+1 < len(args) {
			urls = append(urls, args[i+1])
		}
	}
	if len(urls) != 2 || urls[0] != "https://example.com/a.zip" || urls[1] != "https://example.com/b.zip" {
		t.Errorf("wrong --plugin-url values: %v", urls)
	}
}

func TestParsePromptSuggestionEvent(t *testing.T) {
	line := `{"type":"prompt_suggestion","suggestion":"git status","uuid":"u-1","session_id":"s-1"}`
	events := parseLines(t, line)

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d: %v", len(events), events)
	}
	ev, ok := events[0].(*PromptSuggestionEvent)
	if !ok {
		t.Fatalf("expected *PromptSuggestionEvent, got %T", events[0])
	}
	if ev.Suggestion != "git status" || ev.SessionID != "s-1" || ev.UUID != "u-1" {
		t.Errorf("unexpected event: %+v", ev)
	}
}

func TestParseInitEventMCPServerErrors(t *testing.T) {
	line := `{"type":"system","subtype":"init","session_id":"s-1","model":"claude-opus-5",` +
		`"claude_code_version":"2.1.227","cwd":"/tmp/work","permissionMode":"acceptEdits",` +
		`"output_style":"default","slash_commands":["init"],` +
		`"plugins":[{"name":"ralph-loop","path":"/p/ralph"}],` +
		`"mcp_servers":[{"name":"ok","status":"connected"}],` +
		`"mcp_server_errors":[{"name":"bad","type":"invalid_config","message":"url: expected string"}]}`
	events := parseLines(t, line)

	init, ok := events[0].(*InitEvent)
	if !ok {
		t.Fatalf("expected *InitEvent, got %T", events[0])
	}
	if len(init.MCPServerErrors) != 1 {
		t.Fatalf("expected 1 MCP server error, got %v", init.MCPServerErrors)
	}
	got := init.MCPServerErrors[0]
	if got.Name != "bad" || got.Type != "invalid_config" {
		t.Errorf("unexpected MCP server error: %+v", got)
	}
	if init.CLIVersion != "2.1.227" {
		t.Errorf("CLIVersion = %q", init.CLIVersion)
	}
	if init.CWD != "/tmp/work" {
		t.Errorf("CWD = %q", init.CWD)
	}
	if init.PermissionMode != PermissionAcceptEdits {
		t.Errorf("PermissionMode = %q", init.PermissionMode)
	}
	if init.OutputStyle != "default" {
		t.Errorf("OutputStyle = %q", init.OutputStyle)
	}
	if len(init.SlashCommands) != 1 || init.SlashCommands[0] != "init" {
		t.Errorf("SlashCommands = %v", init.SlashCommands)
	}
	if len(init.Plugins) != 1 || init.Plugins[0].Name != "ralph-loop" || init.Plugins[0].Path != "/p/ralph" {
		t.Errorf("Plugins = %+v", init.Plugins)
	}
}

// Real CLI init payloads omit the fields added for 2.1.219+, so parsing must
// stay clean against an older CLI rather than erroring.
func TestParseInitEventWithoutNewFields(t *testing.T) {
	line := `{"type":"system","subtype":"init","session_id":"s-1","model":"claude-opus-5","tools":["Bash"]}`
	events := parseLines(t, line)

	init, ok := events[0].(*InitEvent)
	if !ok {
		t.Fatalf("expected *InitEvent, got %T (%v)", events[0], events[0])
	}
	if init.MCPServerErrors != nil || init.Plugins != nil || init.CLIVersion != "" {
		t.Errorf("expected zero values for absent fields, got %+v", init)
	}
}

func TestModelDisplayNameFable(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"claude-fable-5", "Fable 5"},
		{"fable", "Fable"},
		{"claude-opus-5", "Opus 5"},
		{"claude-opus-5[1m]", "Opus 5"},
		{"claude-sonnet-5", "Sonnet 5"},
		{"claude-haiku-4-5-20251001", "Haiku 4.5"},
	} {
		if got := ModelDisplayName(tc.in); got != tc.want {
			t.Errorf("ModelDisplayName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// parseLines runs ParseEvents over the given JSONL lines and returns every
// event except activity transitions.
func parseLines(t *testing.T, lines ...string) []Event {
	t.Helper()
	ch := make(chan Event, 64)
	go func() {
		ParseEvents(context.Background(), strings.NewReader(strings.Join(lines, "\n")), ch)
		close(ch)
	}()
	var events []Event
	for ev := range ch {
		if _, ok := ev.(*CLIStateChangeEvent); ok {
			continue
		}
		events = append(events, ev)
	}
	if len(events) == 0 {
		t.Fatal("no events parsed")
	}
	return events
}
