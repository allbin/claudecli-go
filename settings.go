package claudecli

import (
	"encoding/json"
	"fmt"
)

// SettingsSnapshot is the answer to the get_settings control request.
//
// The CLI's Settings shape is large and changes often, so all three views are
// carried as raw JSON rather than modelled: decode the parts you care about.
type SettingsSnapshot struct {
	// Effective is the merged on-disk result across every settings source.
	// Changes made with ApplyFlagSettings show up here.
	Effective json.RawMessage
	// Sources is the raw per-source breakdown in merge order (userSettings,
	// projectSettings, localSettings, flagSettings, policySettings).
	Sources json.RawMessage
	// Applied is the runtime-resolved view, after env overrides, session
	// state, and model-specific defaults. Unlike Effective (a disk merge)
	// this reflects what will actually be sent to the API — it is where
	// applied.effort reports the session's real effort level.
	Applied json.RawMessage
}

// QuerySettings returns the CLI's effective, per-source, and runtime-resolved
// settings via the get_settings control request.
func (s *Session) QuerySettings() (*SettingsSnapshot, error) {
	raw, err := s.sendControlRequestRaw("get_settings", nil)
	if err != nil {
		return nil, err
	}
	var body struct {
		Effective json.RawMessage `json:"effective"`
		Sources   json.RawMessage `json:"sources"`
		Applied   json.RawMessage `json:"applied"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("get_settings: decode response: %w", err)
	}
	return &SettingsSnapshot{
		Effective: body.Effective,
		Sources:   body.Sources,
		Applied:   body.Applied,
	}, nil
}

// AppliedEffort decodes the effort level from the Applied view: the level the
// CLI will send on the session's next API request. It is empty when the
// session's model takes no effort parameter (claude-haiku-4-5, for one), or
// when Applied is missing or undecodable.
func (s *SettingsSnapshot) AppliedEffort() EffortLevel {
	var applied struct {
		Effort *string `json:"effort"`
	}
	if err := json.Unmarshal(s.Applied, &applied); err != nil || applied.Effort == nil {
		return ""
	}
	return EffortLevel(*applied.Effort)
}

// SetEffort changes the reasoning effort for the rest of the session, without
// a restart, and returns the level the CLI resolved afterwards.
//
// It takes effect from the next API request, including mid-turn: a turn
// already running sends its remaining requests at the new level. An empty
// level clears the session's level, falling back to the CLI's default for the
// model. The level is not saved anywhere: it lasts for this session only, so
// pass WithEffort again on resume.
//
// The returned level comes from reading the session back, not from the
// request, because the CLI answers success whether or not it took the level.
// It differs from level when:
//   - level is not one the CLI recognises: the CLI ignores it and the previous
//     level stays in force.
//   - the model takes no effort parameter: the result is empty.
//   - a maxEffortLevel setting, or the model's own ceiling, caps it.
//
// Expect a change to invalidate the prompt cache: on claude-opus-5 and
// claude-sonnet-5 the next request wrote the conversation back into the cache,
// sometimes the whole context. Setting the level already in force kept the
// cache.
//
// Side effect: the CLI records the change in the global config
// (~/.claude.json, or $CLAUDE_CONFIG_DIR/.claude.json) by setting
// unpinOpus47LaunchEffort, unpinOpus48LaunchEffort and unpinFable5LaunchEffort.
// This is the same thing /effort does interactively. From then on, sessions on
// those models started without WithEffort use the effortLevel setting instead
// of the model's default.
//
// Checked against CLI 2.1.270 by recording output_config.effort on the
// requests the CLI sends, for claude-opus-5, claude-fable-5-1 and
// claude-sonnet-5, both between turns and mid-turn. An older CLI that merged
// effortLevel into the flag layer without applying it shows up here as a
// returned level that does not match.
func (s *Session) SetEffort(level EffortLevel) (EffortLevel, error) {
	var value any
	if level != "" {
		value = string(level)
	}
	if err := s.ApplyFlagSettings(map[string]any{"effortLevel": value}); err != nil {
		return "", fmt.Errorf("set effort: %w", err)
	}
	snap, err := s.QuerySettings()
	if err != nil {
		return "", fmt.Errorf("set effort: read back: %w", err)
	}
	return snap.AppliedEffort(), nil
}

// ApplyFlagSettings merges settings into the CLI's flag-settings layer,
// updating the live configuration for the rest of the session.
//
// The flag layer is session-scoped: the change is gone when the session ends,
// and nothing is written to settings files (effortLevel does touch the global
// config — see SetEffort). It accepts any key from the CLI's Settings
// shape, which makes it the only way to change several things mid-session:
//
//   - Permission rules. SetPermissionMode only switches the mode; passing
//     {"permissions": {"allow": [...], "deny": [...], "ask": [...]}} replaces
//     the rules themselves. Verified against CLI 2.1.235: permissions absent
//     from the effective settings before the call are present after it.
//   - "effortLevel", to change reasoning effort without restarting. Prefer
//     SetEffort, which also reads back the level the CLI actually applied.
//   - "ultracode", which is session-scoped by design and has no CLI flag.
//
// Read the result back from QuerySettings().Effective — the flagSettings
// source can still report null after a successful merge.
//
// The CLI applies these without further validation, so a malformed value takes
// effect as given rather than being rejected.
func (s *Session) ApplyFlagSettings(settings map[string]any) error {
	if settings == nil {
		return fmt.Errorf("apply_flag_settings: settings must not be nil")
	}
	return s.sendControlRequest("apply_flag_settings", map[string]any{"settings": settings})
}

// PermissionRules is the shape the CLI expects under the "permissions" key of
// ApplyFlagSettings. Each entry is a rule string such as "Bash(git status:*)"
// or a bare tool name such as "Read".
type PermissionRules struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
	Ask   []string `json:"ask,omitempty"`
}

// SetPermissionRules replaces the session's permission rules. Convenience
// wrapper over ApplyFlagSettings for the common case.
//
// This replaces the flag layer's rules wholesale rather than appending to
// them; pass the full set you want in effect. Rules from settings files remain
// part of the merge.
func (s *Session) SetPermissionRules(rules PermissionRules) error {
	return s.ApplyFlagSettings(map[string]any{"permissions": rules})
}
