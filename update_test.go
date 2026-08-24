package claudecli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// updateRun records what a fake updater was asked to do, so a test can assert
// on the layer that was executed rather than only on the result.
type updateRun struct {
	binary string
	lines  []string
	exit   int
	err    error
}

// fakeUpdateEnv wires a fake install layout to a scripted updater. versions is
// the sequence `claude -v` answers with: the first entry is the pre-update
// probe, the second the post-update re-read.
func fakeUpdateEnv(t *testing.T, files map[string]string, versions []string, run *updateRun) updateEnv {
	t.Helper()
	install := fakeInstallEnv(files)
	install.runVersion = func(context.Context, string) (string, error) {
		if len(versions) == 0 {
			return "", errors.New("version probe failed")
		}
		v := versions[0]
		versions = versions[1:]
		if v == "" {
			return "", errors.New("version probe failed")
		}
		return v, nil
	}
	return updateEnv{
		installEnv: install,
		writable:   func(string) error { return nil },
		runUpdate: func(_ context.Context, binary string, onLine func(string)) (int, error) {
			run.binary = binary
			for _, line := range run.lines {
				onLine(line)
			}
			return run.exit, run.err
		},
	}
}

// nativeLayout is a native install: a launcher symlink on PATH pointing into
// the versioned binaries directory.
func nativeLayout() map[string]string {
	return map[string]string{
		"/home/u/.local/share/claude/versions/2.1.239": elfHeader,
	}
}

func TestUpdateRefusesInstallsTheCLIDoesNotManage(t *testing.T) {
	tests := []struct {
		name        string
		binary      string
		realPath    string
		files       map[string]string
		wantMethod  InstallMethod
		wantCommand string
	}{
		{
			name:        "npm global points at npm",
			realPath:    "/usr/local/lib/node_modules/@anthropic-ai/claude-code/cli.js",
			files:       map[string]string{"/usr/local/lib/node_modules/@anthropic-ai/claude-code/package.json": cliPackageJSON},
			wantMethod:  InstallNPMGlobal,
			wantCommand: "npm install -g @anthropic-ai/claude-code@latest",
		},
		{
			name:        "homebrew points at brew",
			realPath:    "/opt/homebrew/Caskroom/claude-code/2.1.87/claude",
			wantMethod:  InstallPackageManager,
			wantCommand: "brew upgrade claude-code",
		},
		{
			name:        "winget points at winget",
			realPath:    `C:\Users\u\AppData\Local\Microsoft\WinGet\Packages\Anthropic.ClaudeCode\claude.exe`,
			wantMethod:  InstallPackageManager,
			wantCommand: "winget upgrade Anthropic.ClaudeCode",
		},
		{
			name:        "version manager has no safe command",
			realPath:    "/home/u/.asdf/shims/claude",
			wantMethod:  InstallVersionManager,
			wantCommand: "",
		},
		{
			name:        "asdf package layout has no safe command",
			realPath:    "/home/u/.asdf/installs/claude/2.1.87/bin/claude",
			wantMethod:  InstallPackageManager,
			wantCommand: "",
		},
		{
			name:        "unknown has no safe command",
			realPath:    "/opt/mystery/claude",
			wantMethod:  InstallUnknown,
			wantCommand: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var run updateRun
			env := fakeUpdateEnv(t, tt.files, []string{"2.1.87"}, &run)
			env.lookPath = func(string) (string, error) { return tt.realPath, nil }

			_, err := runUpdate(context.Background(), "claude", env, nil)
			if !errors.Is(err, ErrManualUpdate) {
				t.Fatalf("err = %v, want ErrManualUpdate", err)
			}
			var manual *ManualUpdateError
			if !errors.As(err, &manual) {
				t.Fatalf("errors.As(%v, *ManualUpdateError) = false", err)
			}
			if manual.Method != tt.wantMethod {
				t.Errorf("Method = %q, want %q", manual.Method, tt.wantMethod)
			}
			if manual.Command != tt.wantCommand {
				t.Errorf("Command = %q, want %q", manual.Command, tt.wantCommand)
			}
			if run.binary != "" {
				t.Errorf("updater ran %q; a refused install must not be touched", run.binary)
			}
		})
	}
}

func TestUpdateAcceptsSelfManagedInstalls(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		realPath string
		files    map[string]string
		want     InstallMethod
	}{
		{
			name:     "native versions layout",
			path:     "/home/u/.local/bin/claude",
			realPath: "/home/u/.local/share/claude/versions/2.1.239",
			files:    nativeLayout(),
			want:     InstallNative,
		},
		{
			name:     "npm local wrapper",
			path:     "/home/u/.claude/local/claude",
			realPath: "/home/u/.claude/local/claude",
			want:     InstallNPMLocal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var run updateRun
			env := fakeUpdateEnv(t, tt.files, []string{"2.1.239", "2.1.241"}, &run)
			env.lookPath = func(string) (string, error) { return tt.path, nil }
			env.evalSymlink = func(string) (string, error) { return tt.realPath, nil }

			result, err := runUpdate(context.Background(), "claude", env, nil)
			if err != nil {
				t.Fatalf("runUpdate: %v", err)
			}
			if result.Method != tt.want {
				t.Errorf("Method = %q, want %q", result.Method, tt.want)
			}
			if !result.Changed {
				t.Errorf("Changed = false, want true (2.1.239 -> 2.1.241)")
			}
			if result.VersionBefore != "2.1.239" || result.VersionAfter != "2.1.241" {
				t.Errorf("versions = %q -> %q, want 2.1.239 -> 2.1.241", result.VersionBefore, result.VersionAfter)
			}
		})
	}
}

// A machine with two installs is the case a re-lookup at exec time gets wrong:
// the update must reach the copy detection found, not whatever PATH answers a
// second time.
func TestUpdateExecutesTheDetectedPathNotAFreshLookup(t *testing.T) {
	const (
		native    = "/home/u/.local/bin/claude" // first on PATH, native 2.1.239
		npm       = "/usr/local/bin/claude"     // the other copy, npm-global 2.0.14
		versioned = "/home/u/.local/share/claude/versions/2.1.239"
	)

	var run updateRun
	env := fakeUpdateEnv(t, nativeLayout(), []string{"2.1.239", "2.1.241"}, &run)

	lookups := 0
	env.lookPath = func(string) (string, error) {
		lookups++
		if lookups == 1 {
			return native, nil
		}
		// A second lookup reaching the other copy is exactly the bug.
		return npm, nil
	}
	env.evalSymlink = func(p string) (string, error) {
		if p == native {
			return versioned, nil
		}
		return p, nil
	}

	if _, err := runUpdate(context.Background(), "claude", env, nil); err != nil {
		t.Fatalf("runUpdate: %v", err)
	}
	if run.binary != native {
		t.Errorf("executed %q, want the detected PATH entry %q", run.binary, native)
	}
	if run.binary == versioned {
		t.Errorf("executed the symlink-resolved binary; the PATH entry is the layer that must run")
	}
}

func TestUpdateWritabilityPreflight(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		realPath string
		files    map[string]string
		wantDir  string
	}{
		{
			name:     "native checks the versions directory, not the launcher directory",
			path:     "/home/u/.local/bin/claude",
			realPath: "/home/u/.local/share/claude/versions/2.1.239",
			files:    nativeLayout(),
			wantDir:  filepath.FromSlash("/home/u/.local/share/claude/versions"),
		},
		{
			name:     "npm local checks the managed package root",
			path:     "/home/u/.claude/local/claude",
			realPath: "/home/u/.claude/local/node_modules/@anthropic-ai/claude-code/cli.js",
			files:    map[string]string{"/home/u/.claude/local/node_modules/@anthropic-ai/claude-code/package.json": cliPackageJSON},
			wantDir:  filepath.FromSlash("/home/u/.claude/local"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var run updateRun
			env := fakeUpdateEnv(t, tt.files, []string{"2.1.239", "2.1.241"}, &run)
			env.lookPath = func(string) (string, error) { return tt.path, nil }
			env.evalSymlink = func(string) (string, error) { return tt.realPath, nil }

			var checked string
			env.writable = func(dir string) error {
				checked = dir
				return os.ErrPermission
			}

			_, err := runUpdate(context.Background(), "claude", env, nil)
			if !errors.Is(err, ErrUpdateNotWritable) {
				t.Fatalf("err = %v, want ErrUpdateNotWritable", err)
			}
			if checked != tt.wantDir {
				t.Errorf("probed %q, want %q", checked, tt.wantDir)
			}
			var notWritable *UpdateNotWritableError
			if !errors.As(err, &notWritable) {
				t.Fatalf("errors.As(%v, *UpdateNotWritableError) = false", err)
			}
			if notWritable.Dir != tt.wantDir {
				t.Errorf("Dir = %q, want %q", notWritable.Dir, tt.wantDir)
			}
			if !errors.Is(err, os.ErrPermission) {
				t.Errorf("underlying error not unwrapped: %v", err)
			}
			if run.binary != "" {
				t.Errorf("updater ran %q; an unwritable target must not be attempted", run.binary)
			}
		})
	}
}

// "Cannot" and "failed" are different answers to a consumer: one hides the
// button, the other reports an error after it was clicked.
func TestUpdateNotWritableIsDistinctFromUpdateFailed(t *testing.T) {
	if errors.Is(&UpdateNotWritableError{}, ErrUpdateFailed) {
		t.Error("a non-writable target must not read as a failed update")
	}
	if errors.Is(&UpdateFailedError{}, ErrUpdateNotWritable) {
		t.Error("a failed update must not read as a non-writable target")
	}
	if errors.Is(&ManualUpdateError{}, ErrUpdateFailed) {
		t.Error("a manual-update install must not read as a failure")
	}
}

// The exit code is not the answer: a sibling SDK measured an updater exiting 0
// while its own updater command was missing entirely.
func TestUpdateReportsUnchangedWhenAnApparentlySuccessfulRunDidNothing(t *testing.T) {
	var run updateRun
	run.lines = []string{"Update ran successfully"}
	env := fakeUpdateEnv(t, nativeLayout(), []string{"2.1.239", "2.1.239"}, &run)
	env.lookPath = func(string) (string, error) { return "/home/u/.local/bin/claude", nil }
	env.evalSymlink = func(string) (string, error) { return "/home/u/.local/share/claude/versions/2.1.239", nil }

	result, err := runUpdate(context.Background(), "claude", env, nil)
	if err != nil {
		t.Fatalf("runUpdate: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", result.ExitCode)
	}
	if result.Changed {
		t.Error("Changed = true; the version did not move, whatever the updater printed")
	}
	if !strings.Contains(result.Output, "Update ran successfully") {
		t.Errorf("Output = %q, want the updater's own claim kept for diagnosis", result.Output)
	}
}

func TestUpdateFailureKeepsTheResult(t *testing.T) {
	var run updateRun
	run.exit = 1
	run.err = errors.New("exit status 1")
	run.lines = []string{"downloading", "curl: not found"}
	env := fakeUpdateEnv(t, nativeLayout(), []string{"2.1.239", "2.1.239"}, &run)
	env.lookPath = func(string) (string, error) { return "/home/u/.local/bin/claude", nil }
	env.evalSymlink = func(string) (string, error) { return "/home/u/.local/share/claude/versions/2.1.239", nil }

	result, err := runUpdate(context.Background(), "claude", env, nil)
	if !errors.Is(err, ErrUpdateFailed) {
		t.Fatalf("err = %v, want ErrUpdateFailed", err)
	}
	if result == nil {
		t.Fatal("result = nil; a failed run still has before/after versions to report")
	}
	if result.VersionBefore != "2.1.239" || result.VersionAfter != "2.1.239" {
		t.Errorf("versions = %q -> %q, want both 2.1.239", result.VersionBefore, result.VersionAfter)
	}
	var failed *UpdateFailedError
	if !errors.As(err, &failed) {
		t.Fatalf("errors.As(%v, *UpdateFailedError) = false", err)
	}
	if failed.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", failed.ExitCode)
	}
	if !strings.Contains(failed.Error(), "curl: not found") {
		t.Errorf("Error() = %q, want the last output line", failed.Error())
	}
}

// An update nothing can verify must not read as a success.
func TestUpdateErrorsWhenTheVersionCannotBeReRead(t *testing.T) {
	var run updateRun
	env := fakeUpdateEnv(t, nativeLayout(), []string{"2.1.239", ""}, &run)
	env.lookPath = func(string) (string, error) { return "/home/u/.local/bin/claude", nil }
	env.evalSymlink = func(string) (string, error) { return "/home/u/.local/share/claude/versions/2.1.239", nil }

	result, err := runUpdate(context.Background(), "claude", env, nil)
	if err == nil {
		t.Fatal("err = nil; an unverifiable update must not report success")
	}
	if result.VersionAfter != "" {
		t.Errorf("VersionAfter = %q, want empty", result.VersionAfter)
	}
	if result.Changed {
		t.Error("Changed = true with no version to compare against")
	}
}

func TestUpdateStreamsOutput(t *testing.T) {
	var run updateRun
	run.lines = []string{"checking for updates", "downloading 2.1.241", "installed"}
	env := fakeUpdateEnv(t, nativeLayout(), []string{"2.1.239", "2.1.241"}, &run)
	env.lookPath = func(string) (string, error) { return "/home/u/.local/bin/claude", nil }
	env.evalSymlink = func(string) (string, error) { return "/home/u/.local/share/claude/versions/2.1.239", nil }

	var progress []string
	var sink strings.Builder
	_, err := runUpdate(context.Background(), "claude", env, []UpdateOption{
		WithUpdateProgress(func(line string) { progress = append(progress, line) }),
		WithUpdateOutput(&sink),
	})
	if err != nil {
		t.Fatalf("runUpdate: %v", err)
	}
	if strings.Join(progress, "|") != strings.Join(run.lines, "|") {
		t.Errorf("progress = %v, want %v", progress, run.lines)
	}
	if got, want := sink.String(), strings.Join(run.lines, "\n")+"\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestUpdateMissingCLI(t *testing.T) {
	var run updateRun
	env := fakeUpdateEnv(t, nil, nil, &run)
	env.lookPath = func(string) (string, error) { return "", errors.New("not found") }

	if _, err := runUpdate(context.Background(), "claude", env, nil); !errors.Is(err, ErrCLINotFound) {
		t.Fatalf("err = %v, want ErrCLINotFound", err)
	}
}

func TestUpdateTarget(t *testing.T) {
	tests := []struct {
		name   string
		method InstallMethod
		real   string
		env    installEnv
		want   string
	}{
		{
			name:   "native versions directory from the resolved binary",
			method: InstallNative,
			real:   "/home/u/.local/share/claude/versions/2.1.239",
			want:   "/home/u/.local/share/claude/versions",
		},
		{
			name:   "native binary outside the versions layout falls back to the data directory",
			method: InstallNative,
			real:   "/opt/claude/bin/claude",
			env:    installEnv{dataDir: "/home/u/.local/share"},
			want:   "/home/u/.local/share/claude/versions",
		},
		{
			name:   "npm local root from a nested resolved path",
			method: InstallNPMLocal,
			real:   "/home/u/.claude/local/node_modules/@anthropic-ai/claude-code/cli.js",
			want:   "/home/u/.claude/local",
		},
		{
			name:   "npm local root from the wrapper itself",
			method: InstallNPMLocal,
			real:   "/home/u/.claude/local/claude",
			want:   "/home/u/.claude/local",
		},
		{
			name:   "npm local under a relocated config directory",
			method: InstallNPMLocal,
			real:   "/srv/claude-cfg/local/claude",
			env:    installEnv{configDir: "/srv/claude-cfg"},
			want:   "/srv/claude-cfg/local",
		},
		{
			name:   "a method the CLI does not manage has no target",
			method: InstallNPMGlobal,
			real:   "/usr/local/lib/node_modules/@anthropic-ai/claude-code/cli.js",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &InstallInfo{Method: tt.method, RealPath: tt.real}
			want := tt.want
			if want != "" {
				want = filepath.FromSlash(want)
			}
			if got := updateTarget(info, tt.env); got != want {
				t.Errorf("updateTarget = %q, want %q", got, want)
			}
		})
	}
}

func TestCheckWritable(t *testing.T) {
	dir := t.TempDir()
	if err := checkWritable(dir); err != nil {
		t.Fatalf("checkWritable(%q) = %v, want nil", dir, err)
	}
	// The probe must leave nothing behind for a later DetectInstall to read.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("probe left %d entries behind", len(entries))
	}

	if err := checkWritable(filepath.Join(dir, "does-not-exist")); err == nil {
		t.Error("checkWritable on a missing directory = nil, want an error")
	}
	if err := checkWritable(""); err == nil {
		t.Error("checkWritable(\"\") = nil, want an error")
	}

	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("read-only directory permissions are not enforced here")
	}
	readOnly := filepath.Join(dir, "read-only")
	if err := os.Mkdir(readOnly, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := checkWritable(readOnly); err == nil {
		t.Error("checkWritable on a read-only directory = nil, want an error")
	}
}

func TestLineWriter(t *testing.T) {
	tests := []struct {
		name   string
		writes []string
		want   []string
	}{
		{
			name:   "splits on newlines",
			writes: []string{"one\ntwo\n"},
			want:   []string{"one", "two"},
		},
		{
			name:   "splits across writes",
			writes: []string{"par", "tial\nrest\n"},
			want:   []string{"partial", "rest"},
		},
		{
			name:   "carriage returns redrawing in place still narrate",
			writes: []string{"10%\r50%\r100%\n"},
			want:   []string{"10%", "50%", "100%"},
		},
		{
			name:   "trailing text without a break is flushed",
			writes: []string{"no newline"},
			want:   []string{"no newline"},
		},
		{
			name:   "blank lines are dropped",
			writes: []string{"one\n\n\ntwo\n"},
			want:   []string{"one", "two"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			w := &lineWriter{fn: func(line string) { got = append(got, line) }}
			for _, chunk := range tt.writes {
				if n, err := w.Write([]byte(chunk)); n != len(chunk) || err != nil {
					t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
				}
			}
			w.flush()
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("lines = %v, want %v", got, tt.want)
			}
		})
	}
}

// A path that merely contains claude/versions is not the versions directory —
// only one that ends in it is.
func TestNativeVersionsDirRequiresTheLayoutAtTheEnd(t *testing.T) {
	tests := []struct {
		name    string
		real    string
		dataDir string
		want    string
	}{
		{
			name: "the versioned binary itself",
			real: "/home/u/.local/share/claude/versions/2.1.239",
			want: "/home/u/.local/share/claude/versions",
		},
		{
			name:    "nested deeper than the layout allows",
			real:    "/opt/claude/versions/2.1.239/bin/claude",
			dataDir: "/home/u/.local/share",
			want:    "/home/u/.local/share/claude/versions",
		},
		{
			name: "nowhere near the layout and no data directory to fall back on",
			real: "/opt/claude/bin/claude",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.want
			if want != "" {
				want = filepath.FromSlash(want)
			}
			if got := nativeVersionsDir(filepath.FromSlash(tt.real), filepath.FromSlash(tt.dataDir)); got != want {
				t.Errorf("nativeVersionsDir = %q, want %q", got, want)
			}
		})
	}
}
