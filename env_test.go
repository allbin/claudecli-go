package claudecli

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// unsetenv clears key for the test and restores it afterwards.
func unsetenv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	os.Unsetenv(key)
}

func homeKey() string {
	if runtime.GOOS == "windows" {
		return "USERPROFILE"
	}
	return "HOME"
}

func TestProjectsDirProcessEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	got, err := New().ProjectsDir()
	if err != nil || got != filepath.Join(dir, "projects") {
		t.Errorf("ProjectsDir = %q, %v; want %q", got, err, filepath.Join(dir, "projects"))
	}
}

func TestProjectsDirDefaultsToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv(homeKey(), home)
	unsetenv(t, "CLAUDE_CONFIG_DIR")
	got, err := ProjectsDir()
	if want := filepath.Join(home, ".claude", "projects"); err != nil || got != want {
		t.Errorf("ProjectsDir = %q, %v; want %q", got, err, want)
	}

	// A WithEnv HOME relocates ~ for the spawned CLI too.
	other := t.TempDir()
	got, err = New(WithEnv(map[string]string{homeKey(): other})).ProjectsDir()
	if want := filepath.Join(other, ".claude", "projects"); err != nil || got != want {
		t.Errorf("WithEnv HOME: ProjectsDir = %q, %v; want %q", got, err, want)
	}
}

func TestProjectsDirWithEnvOverridesProcess(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	account := t.TempDir()
	c := New(WithModel(ModelHaiku), WithEnv(map[string]string{"CLAUDE_CONFIG_DIR": account}))
	got, err := c.ProjectsDir()
	if want := filepath.Join(account, "projects"); err != nil || got != want {
		t.Errorf("ProjectsDir = %q, %v; want %q", got, err, want)
	}

	// Only the client's defaults count: the last WithEnv wins, as in Run.
	later := t.TempDir()
	c = New(WithEnv(map[string]string{"CLAUDE_CONFIG_DIR": account}),
		WithEnv(map[string]string{"CLAUDE_CONFIG_DIR": later}))
	if got, _ := c.ProjectsDir(); got != filepath.Join(later, "projects") {
		t.Errorf("last WithEnv: ProjectsDir = %q, want under %q", got, later)
	}
}

func TestProjectsDirNotAbsolute(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	cases := map[string]*Client{
		// The spawned CLI receives CLAUDE_CONFIG_DIR= and writes projects/
		// under its working directory; it does not fall back to ~/.claude.
		"WithEnv empty":    New(WithEnv(map[string]string{"CLAUDE_CONFIG_DIR": ""})),
		"WithEnv relative": New(WithEnv(map[string]string{"CLAUDE_CONFIG_DIR": "cfg"})),
	}
	for name, c := range cases {
		if got, err := c.ProjectsDir(); !errors.Is(err, ErrConfigDirNotAbsolute) {
			t.Errorf("%s: ProjectsDir = %q, %v; want ErrConfigDirNotAbsolute", name, got, err)
		}
	}

	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if got, err := New().ProjectsDir(); !errors.Is(err, ErrConfigDirNotAbsolute) {
		t.Errorf("process env empty: ProjectsDir = %q, %v; want ErrConfigDirNotAbsolute", got, err)
	}
}

func TestInstallEnvSeesClientEnv(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	unsetenv(t, AutoUpdateDisabledAutoUpdater)
	account, data := t.TempDir(), t.TempDir()
	c := New(WithEnv(map[string]string{
		"CLAUDE_CONFIG_DIR":           account,
		"XDG_DATA_HOME":               data,
		AutoUpdateDisabledAutoUpdater: "1",
	}))
	env := newInstallEnv(c.cliEnv())
	if env.configDir != account || env.configFile != filepath.Join(account, ".claude.json") {
		t.Errorf("config = %q, %q; want under %q", env.configDir, env.configFile, account)
	}
	if env.dataDir != data {
		t.Errorf("dataDir = %q, want %q", env.dataDir, data)
	}
	if env.env(AutoUpdateDisabledAutoUpdater) != "1" {
		t.Errorf("getenv(%s) = %q, want 1", AutoUpdateDisabledAutoUpdater, env.env(AutoUpdateDisabledAutoUpdater))
	}

	// The probe keeps its degrade-to-default reading of an empty value.
	home := t.TempDir()
	env = newInstallEnv(cliEnv{"CLAUDE_CONFIG_DIR": "", homeKey(): home})
	if env.configDir != filepath.Join(home, ".claude") || env.configFile != filepath.Join(home, ".claude.json") {
		t.Errorf("empty CLAUDE_CONFIG_DIR: config = %q, %q; want the home layout", env.configDir, env.configFile)
	}
}

func TestCLIEnvCmdEnv(t *testing.T) {
	if cliEnv(nil).cmdEnv() != nil {
		t.Error("cmdEnv with no WithEnv entries must be nil to inherit the process env")
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "/process")
	t.Setenv("CLAUDECLI_ENV_TEST_KEEP", "kept")
	got := cliEnv{"CLAUDE_CONFIG_DIR": "/client"}.cmdEnv()
	if slices.Contains(got, "CLAUDE_CONFIG_DIR=/process") || !slices.Contains(got, "CLAUDE_CONFIG_DIR=/client") {
		t.Errorf("CLAUDE_CONFIG_DIR not replaced: %v", got)
	}
	if !slices.Contains(got, "CLAUDECLI_ENV_TEST_KEEP=kept") {
		t.Error("process variables must be kept")
	}
}
