package claudecli

import (
	"encoding/json"
	"fmt"
)

// BackgroundTask backgrounds the foreground task started by the given
// tool_use block — the control-protocol equivalent of pressing Ctrl+B.
//
// The blocking tool call returns immediately with a "running in the
// background" tool_result and the turn continues; the task keeps running and
// emits a task_notification when it settles. Unlike StopTask this is not
// destructive: the work still finishes.
//
// Pass an empty toolUseID to background every foreground task. The reported
// bool is only meaningful when a specific id was given — it is false when no
// matching foreground task was found — and is always false when backgrounding
// everything, since the CLI answers that with an empty body.
func (s *Session) BackgroundTask(toolUseID string) (bool, error) {
	var data map[string]any
	if toolUseID != "" {
		data = map[string]any{"tool_use_id": toolUseID}
	}
	raw, err := s.sendControlRequestRaw("background_tasks", data)
	if err != nil {
		return false, err
	}
	var body struct {
		Backgrounded bool `json:"backgrounded"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	return body.Backgrounded, nil
}

// RenameSession sets the user-facing title of the session.
func (s *Session) RenameSession(title string) error {
	return s.sendControlRequest("rename_session", map[string]any{"title": title})
}

// ThinkingDisplay controls how thinking output is surfaced for the rest of
// the session. Pass one of these to SetMaxThinkingTokens.
type ThinkingDisplay string

const (
	// ThinkingDisplayDefault leaves the display mode from session start
	// (--thinking-display) in place.
	ThinkingDisplayDefault ThinkingDisplay = ""
	// ThinkingDisplaySummarized shows summarized thinking.
	ThinkingDisplaySummarized ThinkingDisplay = "summarized"
	// ThinkingDisplayOmitted hides thinking output.
	ThinkingDisplayOmitted ThinkingDisplay = "omitted"
)

// SetMaxThinkingTokens changes the extended-thinking budget mid-session.
//
// A nil tokens resets to the session default: any mid-session override is
// cleared, back to the spawn-time budget if one was set. Thinking stays off
// for sessions that have it disabled — this cannot switch it on.
//
// display sets the thinking display mode for the rest of the session;
// ThinkingDisplayDefault leaves it as configured at startup.
func (s *Session) SetMaxThinkingTokens(tokens *int, display ThinkingDisplay) error {
	data := map[string]any{"max_thinking_tokens": nil}
	if tokens != nil {
		data["max_thinking_tokens"] = *tokens
	}
	if display != ThinkingDisplayDefault {
		data["thinking_display"] = string(display)
	}
	return s.sendControlRequest("set_max_thinking_tokens", data)
}

// BinaryVersion reports the CLI binary answering this session.
type BinaryVersion struct {
	Version   string `json:"version"`
	BuildTime string `json:"buildTime"`
}

// QueryBinaryVersion asks the CLI for its own version over the control
// channel.
//
// Because it is a real, side-effect-free request-response, it doubles as a
// liveness probe — see Ping.
func (s *Session) QueryBinaryVersion() (*BinaryVersion, error) {
	raw, err := s.sendControlRequestRaw("get_binary_version", nil)
	if err != nil {
		return nil, err
	}
	var v BinaryVersion
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("get_binary_version: decode response: %w", err)
	}
	return &v, nil
}

// ReloadSkills reloads skills from disk and returns the refreshed list.
func (s *Session) ReloadSkills() ([]SlashCommand, error) {
	raw, err := s.sendControlRequestRaw("reload_skills", nil)
	if err != nil {
		return nil, err
	}
	var body struct {
		Skills []SlashCommand `json:"skills"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("reload_skills: decode response: %w", err)
	}
	return body.Skills, nil
}

// SlashCommand describes a slash command or skill available in the session.
type SlashCommand struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	ArgumentHint string `json:"argumentHint,omitempty"`
}

// ReloadPluginsResult is the refreshed session surface after a plugin reload.
type ReloadPluginsResult struct {
	Commands   []SlashCommand    `json:"commands"`
	MCPServers []MCPServerStatus `json:"mcpServers"`
	// ErrorCount is the number of plugins that failed to load.
	ErrorCount int `json:"error_count"`
	// Raw is the full response, which also carries agent and plugin
	// inventories not modelled here.
	Raw json.RawMessage `json:"-"`
}

// ReloadPlugins reloads plugins from disk and returns the refreshed session
// components.
func (s *Session) ReloadPlugins() (*ReloadPluginsResult, error) {
	raw, err := s.sendControlRequestRaw("reload_plugins", nil)
	if err != nil {
		return nil, err
	}
	var result ReloadPluginsResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("reload_plugins: decode response: %w", err)
	}
	result.Raw = append(json.RawMessage(nil), raw...)
	return &result, nil
}
