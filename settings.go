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

// ApplyFlagSettings merges settings into the CLI's flag-settings layer,
// updating the live configuration for the rest of the session.
//
// The flag layer is session-scoped: nothing is written to disk, and the change
// is gone when the session ends. It accepts any key from the CLI's Settings
// shape, which makes it the only way to change several things mid-session:
//
//   - Permission rules. SetPermissionMode only switches the mode; passing
//     {"permissions": {"allow": [...], "deny": [...], "ask": [...]}} replaces
//     the rules themselves. Verified against CLI 2.1.235: permissions absent
//     from the effective settings before the call are present after it.
//   - "effortLevel", to change reasoning effort without restarting.
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
