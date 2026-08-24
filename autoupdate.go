package claudecli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"
)

// Release channels the CLI publishes. These are the only values ever
// interpolated into a lookup URL: the channel arrives from a settings file, and
// a settings-sourced string must not reach a URL unvalidated.
const (
	// ChannelLatest is the default channel — every release, as it ships.
	ChannelLatest = "latest"

	// ChannelStable lags ChannelLatest, typically by days.
	ChannelStable = "stable"

	// ChannelRC is settable in the CLI's own config UI (which labels it
	// "slow"), but no artifact is published under that name: a lookup for it
	// returns 404 from every source. Recognized so it is reported honestly
	// rather than silently rewritten to another channel's number.
	ChannelRC = "rc"
)

// validChannel reports whether a channel name is one the CLI publishes under.
// Anything else is refused rather than interpolated into a URL.
func validChannel(c string) bool {
	switch c {
	case ChannelLatest, ChannelStable, ChannelRC:
		return true
	default:
		return false
	}
}

// Reasons an install's background updater is off, as reported by
// AutoUpdateState.DisabledBy.
const (
	// AutoUpdateDisabledConfig means `autoUpdates: false` in the CLI's config
	// file — usually the user's own choice, not an administrative lock.
	AutoUpdateDisabledConfig = "config"

	// AutoUpdateDisabledAutoUpdater means DISABLE_AUTOUPDATER is set. It stops
	// background updates only; an explicit [Update] still runs.
	AutoUpdateDisabledAutoUpdater = "DISABLE_AUTOUPDATER"

	// AutoUpdateDisabledUpdates means DISABLE_UPDATES is set. This one is an
	// administrative decision and the CLI's own `claude update` refuses under
	// it, so [Update] will fail too.
	AutoUpdateDisabledUpdates = "DISABLE_UPDATES"

	// AutoUpdateDisabledPackageManager means the install belongs to an OS
	// package manager, which owns the upgrade. The CLI reports these as
	// "Managed by package manager" rather than enabled or disabled.
	AutoUpdateDisabledPackageManager = "package-manager"
)

// AutoUpdateState describes the CLI's own background updater for an install.
//
// A native install keeps itself current: it checks its release channel while
// running and swaps in a new versioned binary on its own. A consumer that knows
// this can stay quiet about a version that is already being handled, and can
// explain a stale version by pointing at LastAttempt instead of nagging.
//
// Everything here is read from files DetectInstall already opens plus a handful
// of environment variables. It costs no extra process and no network call —
// which is the whole reason it is not read from `claude doctor`, the command
// that reports the same three facts by rewriting the config file to answer.
type AutoUpdateState struct {
	// Enabled is whether the CLI will update itself in the background. True
	// when nothing turns it off: the preference defaults to on.
	Enabled bool

	// DisabledBy names what turned background updates off, or "" when they
	// are on. One of the AutoUpdateDisabled* constants.
	DisabledBy string

	// Channel is the release channel this install tracks — one of
	// [ChannelLatest], [ChannelStable] or [ChannelRC]. Empty only when the
	// settings file names a channel the CLI does not publish, which is
	// reported as-is rather than silently defaulted.
	//
	// The channels do not agree: measured on one machine on one day, `latest`
	// was 2.1.241 and `stable` 2.1.231. Comparing an installed version against
	// the wrong one manufactures a "behind" that is not true.
	Channel string

	// ChannelSource records where Channel came from: "settings" when the CLI's
	// user settings file names it, "homebrew-cask" when the Homebrew cask name
	// determines it, "default" when nothing does, or "unknown" when the
	// settings file names a channel that is not published.
	ChannelSource string

	// LastAttempt is the CLI's record of its most recent update attempt, or
	// nil when it has never recorded one. It is written by the CLI itself, not
	// by this package.
	LastAttempt *UpdateAttempt
}

// UpdateAttempt is the CLI's own record of one update attempt, read verbatim
// from its last-update-result file.
type UpdateAttempt struct {
	// Time is when the attempt finished, or the zero time when the recorded
	// timestamp could not be parsed.
	Time time.Time

	// Outcome is the CLI's word for how it went ("success", "install_failed",
	// …), reported verbatim rather than normalized — the vocabulary is the
	// CLI's and changes between versions.
	Outcome string

	// From and To are the versions the attempt moved between. To is set even
	// on a failed attempt: it is the version that was being installed.
	From string
	To   string

	// ErrorCode is the CLI's error classification, or "" on success.
	ErrorCode string

	// InstallPath is the CLI's word for which install layout it updated
	// ("native", …), verbatim.
	InstallPath string
}

// Succeeded reports whether this attempt completed without error.
func (a *UpdateAttempt) Succeeded() bool { return a != nil && a.Outcome == "success" }

// lastUpdateResultFile is the CLI's record of its most recent update attempt,
// written next to the config directory.
const lastUpdateResultFile = ".last-update-result.json"

// settingsFileName is the CLI's user-scope settings file, inside the config
// directory. It is where the `/channel` command writes autoUpdatesChannel.
const settingsFileName = "settings.json"

// readAutoUpdateState assembles the background-updater picture from the config
// file already read, the user settings file, the last-update record and the
// environment. Every input is optional: a missing or malformed file leaves its
// part of the state at its default rather than failing detection.
func readAutoUpdateState(env installEnv, info *InstallInfo, cfg claudeConfig) *AutoUpdateState {
	state := &AutoUpdateState{Enabled: true}

	// Precedence mirrors the CLI: an OS package manager owns the upgrade
	// outright, then the administrative env var, then the softer one, then the
	// user's own recorded preference.
	switch {
	case info.Method == InstallPackageManager:
		state.Enabled, state.DisabledBy = false, AutoUpdateDisabledPackageManager
	case env.env(AutoUpdateDisabledUpdates) != "":
		state.Enabled, state.DisabledBy = false, AutoUpdateDisabledUpdates
	case env.env(AutoUpdateDisabledAutoUpdater) != "":
		state.Enabled, state.DisabledBy = false, AutoUpdateDisabledAutoUpdater
	case cfg.AutoUpdates != nil && !*cfg.AutoUpdates:
		state.Enabled, state.DisabledBy = false, AutoUpdateDisabledConfig
	}

	state.Channel, state.ChannelSource = resolveChannel(env, info)
	state.LastAttempt = readLastUpdateAttempt(env)
	return state
}

// resolveChannel reports which release channel an install tracks.
//
// Homebrew is the exception the CLI itself carves out: a brew install picks its
// channel by cask name, not by settings, so the `claude-code` cask tracks
// stable and `claude-code@latest` tracks latest regardless of what settings
// say. Everything else reads `autoUpdatesChannel` from the CLI's user settings
// file and defaults to latest.
//
// Only the user-scope settings file is consulted. Project, local and managed
// settings can also carry the key, but the CLI's own channel command writes the
// user scope, and reading the full cascade would mean re-implementing a
// platform-specific merge order for a value that is user-scoped in practice.
func resolveChannel(env installEnv, info *InstallInfo) (channel, source string) {
	if info.PackageManager == "homebrew" {
		switch info.PackageName {
		case "claude-code":
			return ChannelStable, "homebrew-cask"
		case "claude-code@latest":
			return ChannelLatest, "homebrew-cask"
		}
		// An unrecognized cask name says nothing about the channel; fall
		// through rather than guess which one it tracks.
	}

	switch c := readSettingsChannel(env); {
	case c == "":
		return ChannelLatest, "default"
	case validChannel(c):
		return c, "settings"
	default:
		return "", "unknown"
	}
}

// readSettingsChannel reads `autoUpdatesChannel` from the CLI's user settings
// file. Any failure reads as "" — an unreadable settings file means "not
// configured", never a detection failure.
func readSettingsChannel(env installEnv) string {
	if env.configDir == "" || env.readFile == nil {
		return ""
	}
	b, err := env.readFile(filepath.Join(env.configDir, settingsFileName))
	if err != nil {
		return ""
	}
	var settings struct {
		AutoUpdatesChannel string `json:"autoUpdatesChannel"`
	}
	if json.Unmarshal(b, &settings) != nil {
		return ""
	}
	return strings.TrimSpace(settings.AutoUpdatesChannel)
}

// readLastUpdateAttempt reads the CLI's record of its most recent update
// attempt, or nil when there is none to read.
func readLastUpdateAttempt(env installEnv) *UpdateAttempt {
	if env.configDir == "" || env.readFile == nil {
		return nil
	}
	b, err := env.readFile(filepath.Join(env.configDir, lastUpdateResultFile))
	if err != nil {
		return nil
	}
	var rec struct {
		Timestamp   string `json:"timestamp"`
		Path        string `json:"path"`
		Outcome     string `json:"outcome"`
		VersionFrom string `json:"version_from"`
		VersionTo   string `json:"version_to"`
		ErrorCode   string `json:"error_code"`
	}
	if json.Unmarshal(b, &rec) != nil {
		return nil
	}
	if rec.Outcome == "" && rec.Timestamp == "" {
		return nil
	}
	attempt := &UpdateAttempt{
		Outcome:     rec.Outcome,
		From:        rec.VersionFrom,
		To:          rec.VersionTo,
		ErrorCode:   rec.ErrorCode,
		InstallPath: rec.Path,
	}
	// A timestamp we cannot parse leaves Time zero rather than dropping the
	// attempt: the outcome is the useful part.
	if t, err := time.Parse(time.RFC3339, rec.Timestamp); err == nil {
		attempt.Time = t
	}
	return attempt
}
