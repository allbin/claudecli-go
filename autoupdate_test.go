package claudecli

import (
	"context"
	"testing"
	"time"
)

// lastUpdateResult is the record the CLI writes after an update attempt,
// captured verbatim from a native install.
const lastUpdateResult = `{"timestamp":"2026-08-22T07:13:21.473Z","path":"native","outcome":"success","status":"success","version_from":"2.1.235","version_to":"2.1.239","error_code":null}`

func TestAutoUpdateEnabledState(t *testing.T) {
	disabled := `{"installMethod":"native","autoUpdates":false}`
	enabled := `{"installMethod":"native","autoUpdates":true}`

	tests := []struct {
		name       string
		realPath   string
		config     string
		env        map[string]string
		wantOn     bool
		wantReason string
	}{
		{
			name:     "absent preference means enabled",
			realPath: "/home/u/.local/share/claude/versions/2.1.239",
			wantOn:   true,
		},
		{
			name:     "explicitly enabled",
			realPath: "/home/u/.local/share/claude/versions/2.1.239",
			config:   enabled,
			wantOn:   true,
		},
		{
			name:       "turned off in the config",
			realPath:   "/home/u/.local/share/claude/versions/2.1.239",
			config:     disabled,
			wantReason: AutoUpdateDisabledConfig,
		},
		{
			name:       "DISABLE_AUTOUPDATER stops background updates",
			realPath:   "/home/u/.local/share/claude/versions/2.1.239",
			env:        map[string]string{"DISABLE_AUTOUPDATER": "1"},
			wantReason: AutoUpdateDisabledAutoUpdater,
		},
		{
			name:       "DISABLE_UPDATES outranks the softer variable",
			realPath:   "/home/u/.local/share/claude/versions/2.1.239",
			env:        map[string]string{"DISABLE_UPDATES": "1", "DISABLE_AUTOUPDATER": "1"},
			wantReason: AutoUpdateDisabledUpdates,
		},
		{
			name:       "a package manager owns the upgrade outright",
			realPath:   "/opt/homebrew/Caskroom/claude-code/2.1.231/claude",
			wantReason: AutoUpdateDisabledPackageManager,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{"/home/u/.local/share/claude/versions/2.1.239": elfHeader}
			if tt.config != "" {
				files["/home/u/.claude.json"] = tt.config
			}
			env := fakeInstallEnv(files)
			env.lookPath = func(string) (string, error) { return tt.realPath, nil }
			env.getenv = func(name string) string { return tt.env[name] }

			info, err := detectInstall(context.Background(), "claude", env)
			if err != nil {
				t.Fatalf("detectInstall: %v", err)
			}
			if info.AutoUpdate == nil {
				t.Fatal("AutoUpdate = nil, want a state on every install")
			}
			if info.AutoUpdate.Enabled != tt.wantOn {
				t.Errorf("Enabled = %v, want %v", info.AutoUpdate.Enabled, tt.wantOn)
			}
			if info.AutoUpdate.DisabledBy != tt.wantReason {
				t.Errorf("DisabledBy = %q, want %q", info.AutoUpdate.DisabledBy, tt.wantReason)
			}
		})
	}
}

func TestAutoUpdateChannelResolution(t *testing.T) {
	tests := []struct {
		name       string
		realPath   string
		settings   string
		wantChan   string
		wantSource string
	}{
		{
			name:       "no settings file means latest",
			realPath:   "/home/u/.local/share/claude/versions/2.1.239",
			wantChan:   ChannelLatest,
			wantSource: "default",
		},
		{
			name:       "settings without the key mean latest",
			realPath:   "/home/u/.local/share/claude/versions/2.1.239",
			settings:   `{"theme":"dark"}`,
			wantChan:   ChannelLatest,
			wantSource: "default",
		},
		{
			name:       "settings naming stable",
			realPath:   "/home/u/.local/share/claude/versions/2.1.239",
			settings:   settingsWithChannel(ChannelStable),
			wantChan:   ChannelStable,
			wantSource: "settings",
		},
		{
			name:       "settings naming rc, which the CLI accepts but does not publish under",
			realPath:   "/home/u/.local/share/claude/versions/2.1.239",
			settings:   settingsWithChannel(ChannelRC),
			wantChan:   ChannelRC,
			wantSource: "settings",
		},
		{
			name:       "a channel the CLI does not publish is reported, not defaulted",
			realPath:   "/home/u/.local/share/claude/versions/2.1.239",
			settings:   settingsWithChannel("nightly"),
			wantChan:   "",
			wantSource: "unknown",
		},
		{
			name:       "malformed settings read as not configured",
			realPath:   "/home/u/.local/share/claude/versions/2.1.239",
			settings:   `{not json`,
			wantChan:   ChannelLatest,
			wantSource: "default",
		},
		{
			name:       "the stable homebrew cask decides its own channel",
			realPath:   "/opt/homebrew/Caskroom/claude-code/2.1.231/claude",
			settings:   settingsWithChannel(ChannelLatest),
			wantChan:   ChannelStable,
			wantSource: "homebrew-cask",
		},
		{
			name:       "the latest homebrew cask decides its own channel",
			realPath:   "/opt/homebrew/Caskroom/claude-code@latest/2.1.241/claude",
			settings:   settingsWithChannel(ChannelStable),
			wantChan:   ChannelLatest,
			wantSource: "homebrew-cask",
		},
		{
			name:       "an unrecognized cask falls back to the settings channel",
			realPath:   "/opt/homebrew/Caskroom/claude-code-beta/2.1.87/claude",
			settings:   settingsWithChannel(ChannelStable),
			wantChan:   ChannelStable,
			wantSource: "settings",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{"/home/u/.local/share/claude/versions/2.1.239": elfHeader}
			if tt.settings != "" {
				files["/home/u/.claude/settings.json"] = tt.settings
			}
			env := fakeInstallEnv(files)
			env.lookPath = func(string) (string, error) { return tt.realPath, nil }

			info, err := detectInstall(context.Background(), "claude", env)
			if err != nil {
				t.Fatalf("detectInstall: %v", err)
			}
			if info.AutoUpdate.Channel != tt.wantChan {
				t.Errorf("Channel = %q, want %q", info.AutoUpdate.Channel, tt.wantChan)
			}
			if info.AutoUpdate.ChannelSource != tt.wantSource {
				t.Errorf("ChannelSource = %q, want %q", info.AutoUpdate.ChannelSource, tt.wantSource)
			}
		})
	}
}

func TestAutoUpdateLastAttempt(t *testing.T) {
	env := fakeInstallEnv(map[string]string{
		"/home/u/.local/share/claude/versions/2.1.239": elfHeader,
		"/home/u/.claude/.last-update-result.json":     lastUpdateResult,
	})
	env.lookPath = func(string) (string, error) { return "/home/u/.local/share/claude/versions/2.1.239", nil }

	info, err := detectInstall(context.Background(), "claude", env)
	if err != nil {
		t.Fatalf("detectInstall: %v", err)
	}
	attempt := info.AutoUpdate.LastAttempt
	if attempt == nil {
		t.Fatal("LastAttempt = nil, want the CLI's own record")
	}
	if !attempt.Succeeded() {
		t.Errorf("Succeeded() = false, outcome %q", attempt.Outcome)
	}
	if attempt.From != "2.1.235" || attempt.To != "2.1.239" {
		t.Errorf("versions = %q -> %q, want 2.1.235 -> 2.1.239", attempt.From, attempt.To)
	}
	if attempt.InstallPath != "native" {
		t.Errorf("InstallPath = %q, want %q", attempt.InstallPath, "native")
	}
	want := time.Date(2026, 8, 22, 7, 13, 21, 473000000, time.UTC)
	if !attempt.Time.Equal(want) {
		t.Errorf("Time = %v, want %v", attempt.Time, want)
	}
}

func TestAutoUpdateLastAttemptTolerance(t *testing.T) {
	tests := []struct {
		name    string
		record  string
		wantNil bool
		check   func(*testing.T, *UpdateAttempt)
	}{
		{
			name:    "no record at all",
			wantNil: true,
		},
		{
			name:    "malformed record",
			record:  `{oops`,
			wantNil: true,
		},
		{
			name:    "empty object carries nothing worth reporting",
			record:  `{}`,
			wantNil: true,
		},
		{
			name:   "an unparseable timestamp keeps the outcome",
			record: `{"timestamp":"yesterday","outcome":"install_failed","error_code":"network_timeout"}`,
			check: func(t *testing.T, a *UpdateAttempt) {
				if a.Succeeded() {
					t.Error("Succeeded() = true for a failed attempt")
				}
				if a.ErrorCode != "network_timeout" {
					t.Errorf("ErrorCode = %q, want %q", a.ErrorCode, "network_timeout")
				}
				if !a.Time.IsZero() {
					t.Errorf("Time = %v, want the zero time", a.Time)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{"/home/u/.local/share/claude/versions/2.1.239": elfHeader}
			if tt.record != "" {
				files["/home/u/.claude/.last-update-result.json"] = tt.record
			}
			env := fakeInstallEnv(files)
			env.lookPath = func(string) (string, error) { return "/home/u/.local/share/claude/versions/2.1.239", nil }

			info, err := detectInstall(context.Background(), "claude", env)
			if err != nil {
				t.Fatalf("detectInstall: %v", err)
			}
			attempt := info.AutoUpdate.LastAttempt
			if tt.wantNil {
				if attempt != nil {
					t.Fatalf("LastAttempt = %+v, want nil", attempt)
				}
				return
			}
			if attempt == nil {
				t.Fatal("LastAttempt = nil")
			}
			tt.check(t, attempt)
		})
	}
}

// A relocated config directory has to be followed, or the state silently reads
// as "never updated" on every machine that sets CLAUDE_CONFIG_DIR.
func TestAutoUpdateFollowsARelocatedConfigDirectory(t *testing.T) {
	env := fakeInstallEnv(map[string]string{
		"/home/u/.local/share/claude/versions/2.1.239": elfHeader,
		"/srv/claude-cfg/settings.json":                settingsWithChannel(ChannelStable),
		"/srv/claude-cfg/.last-update-result.json":     lastUpdateResult,
	})
	env.lookPath = func(string) (string, error) { return "/home/u/.local/share/claude/versions/2.1.239", nil }
	env.configDir = "/srv/claude-cfg"
	env.configFile = "/srv/claude-cfg/.claude.json"

	info, err := detectInstall(context.Background(), "claude", env)
	if err != nil {
		t.Fatalf("detectInstall: %v", err)
	}
	if info.AutoUpdate.Channel != ChannelStable {
		t.Errorf("Channel = %q, want %q", info.AutoUpdate.Channel, ChannelStable)
	}
	if info.AutoUpdate.LastAttempt == nil {
		t.Error("LastAttempt = nil, want the record from the relocated directory")
	}
}

func TestValidChannel(t *testing.T) {
	for _, c := range []string{ChannelLatest, ChannelStable, ChannelRC} {
		if !validChannel(c) {
			t.Errorf("validChannel(%q) = false, want true", c)
		}
	}
	for _, c := range []string{"", "next", "nightly", "../stable", "LATEST"} {
		if validChannel(c) {
			t.Errorf("validChannel(%q) = true, want false", c)
		}
	}
}
