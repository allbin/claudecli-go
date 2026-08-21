package claudecli

import (
	"context"
	"testing"
)

// runControl drives one control request against the simulator and hands back
// the request the CLI would have seen.
func runControl(t *testing.T, body string, call func(*Session) error) map[string]any {
	t.Helper()
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	got := make(chan map[string]any, 1)
	go func() {
		sim.handleInitAndReady(t)
		msg := sim.respondSuccessWithBody(t, body)
		got <- msg["request"].(map[string]any)
		sim.sendResult()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	if err := call(session); err != nil {
		t.Fatalf("control call failed: %v", err)
	}
	request := <-got
	if _, err := session.Wait(); err != nil {
		t.Fatal(err)
	}
	return request
}

func TestBackgroundTaskTargeted(t *testing.T) {
	var backgrounded bool
	req := runControl(t, `{"backgrounded":true}`, func(s *Session) error {
		var err error
		backgrounded, err = s.BackgroundTask("toolu_42")
		return err
	})
	if req["subtype"] != "background_tasks" {
		t.Fatalf("subtype = %v", req["subtype"])
	}
	if req["tool_use_id"] != "toolu_42" {
		t.Errorf("tool_use_id = %v", req["tool_use_id"])
	}
	if !backgrounded {
		t.Error("backgrounded = false, want true")
	}
}

// Backgrounding everything is Ctrl+B semantics and must omit tool_use_id —
// sending an empty string would target a task named "".
func TestBackgroundTaskAllOmitsToolUseID(t *testing.T) {
	req := runControl(t, `{}`, func(s *Session) error {
		_, err := s.BackgroundTask("")
		return err
	})
	if _, present := req["tool_use_id"]; present {
		t.Errorf("tool_use_id sent when backgrounding all: %v", req["tool_use_id"])
	}
}

func TestRenameSession(t *testing.T) {
	req := runControl(t, `{}`, func(s *Session) error {
		return s.RenameSession("my session")
	})
	if req["subtype"] != "rename_session" || req["title"] != "my session" {
		t.Errorf("request = %#v", req)
	}
}

func TestQueryBinaryVersion(t *testing.T) {
	var got *BinaryVersion
	req := runControl(t, `{"version":"2.1.235","buildTime":"2026-08-19T00:00:00Z"}`, func(s *Session) error {
		var err error
		got, err = s.QueryBinaryVersion()
		return err
	})
	if req["subtype"] != "get_binary_version" {
		t.Fatalf("subtype = %v", req["subtype"])
	}
	if got.Version != "2.1.235" {
		t.Errorf("Version = %q", got.Version)
	}
}

// A nil budget resets to the session default and must be sent as an explicit
// null — omitting the key would leave any override in place.
func TestSetMaxThinkingTokensReset(t *testing.T) {
	req := runControl(t, `{}`, func(s *Session) error {
		return s.SetMaxThinkingTokens(nil, ThinkingDisplayDefault)
	})
	if req["subtype"] != "set_max_thinking_tokens" {
		t.Fatalf("subtype = %v", req["subtype"])
	}
	v, present := req["max_thinking_tokens"]
	if !present {
		t.Fatal("max_thinking_tokens omitted; want explicit null")
	}
	if v != nil {
		t.Errorf("max_thinking_tokens = %v, want null", v)
	}
	if _, present := req["thinking_display"]; present {
		t.Error("default display mode was serialized")
	}
}

func TestSetMaxThinkingTokensValue(t *testing.T) {
	budget := 4096
	req := runControl(t, `{}`, func(s *Session) error {
		return s.SetMaxThinkingTokens(&budget, ThinkingDisplayOmitted)
	})
	if req["max_thinking_tokens"] != float64(4096) {
		t.Errorf("max_thinking_tokens = %v", req["max_thinking_tokens"])
	}
	if req["thinking_display"] != "omitted" {
		t.Errorf("thinking_display = %v", req["thinking_display"])
	}
}

func TestReloadSkills(t *testing.T) {
	var skills []SlashCommand
	req := runControl(t, `{"skills":[{"name":"code-review","description":"Review changes","argumentHint":"since"}]}`,
		func(s *Session) error {
			var err error
			skills, err = s.ReloadSkills()
			return err
		})
	if req["subtype"] != "reload_skills" {
		t.Fatalf("subtype = %v", req["subtype"])
	}
	if len(skills) != 1 || skills[0].Name != "code-review" {
		t.Errorf("skills = %+v", skills)
	}
}

func TestReloadPlugins(t *testing.T) {
	var result *ReloadPluginsResult
	req := runControl(t, `{"commands":[{"name":"c","description":"d"}],"mcpServers":[{"name":"m","status":"connected"}],"error_count":2,"plugins":[{"name":"p","path":"/x"}]}`,
		func(s *Session) error {
			var err error
			result, err = s.ReloadPlugins()
			return err
		})
	if req["subtype"] != "reload_plugins" {
		t.Fatalf("subtype = %v", req["subtype"])
	}
	if result.ErrorCount != 2 {
		t.Errorf("ErrorCount = %d", result.ErrorCount)
	}
	if len(result.Commands) != 1 || len(result.MCPServers) != 1 {
		t.Errorf("result = %+v", result)
	}
	// The plugin inventory is not modelled but must survive in Raw.
	if len(result.Raw) == 0 {
		t.Error("Raw empty")
	}
}
