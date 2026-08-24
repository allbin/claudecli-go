package claudecli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pathLayout describes a synthetic machine: which PATH directories exist, which
// of them hold a claude, and what each one resolves to.
type pathLayout struct {
	dirs     []string          // PATH, in order
	binaries map[string]string // "<dir>/claude" -> resolved real path
	files    map[string]string // classification inputs for the resolved paths
}

func (l pathLayout) env(t *testing.T) installEnv {
	t.Helper()
	env := fakeInstallEnv(l.files)
	env.pathDirs = func() []string { return l.dirs }
	env.lookPath = func(name string) (string, error) {
		// A bare name resolves the way exec.LookPath would: first hit wins.
		if filepath.Base(name) == name {
			for _, dir := range l.dirs {
				if _, ok := l.binaries[filepath.Join(dir, name)]; ok {
					return filepath.Join(dir, name), nil
				}
			}
			return "", errors.New("not found")
		}
		if _, ok := l.binaries[name]; ok {
			return name, nil
		}
		return "", os.ErrNotExist
	}
	env.evalSymlink = func(p string) (string, error) {
		if real, ok := l.binaries[p]; ok {
			return real, nil
		}
		return p, nil
	}
	return env
}

// twoCopies is the state measured on an ordinary machine: a current native
// install winning over a fifteen-month-old npm-global one.
func twoCopies() pathLayout {
	return pathLayout{
		dirs: []string{"/home/u/.local/bin", "/usr/local/bin", "/usr/bin"},
		binaries: map[string]string{
			"/home/u/.local/bin/claude": "/home/u/.local/share/claude/versions/2.1.241",
			"/usr/local/bin/claude":     "/usr/local/lib/node_modules/@anthropic-ai/claude-code/cli.js",
		},
		files: map[string]string{
			"/home/u/.local/share/claude/versions/2.1.241":                       elfHeader,
			"/usr/local/lib/node_modules/@anthropic-ai/claude-code/package.json": cliPackageJSON,
		},
	}
}

func TestDetectInstallReportsEveryCopyOnPath(t *testing.T) {
	info, err := detectInstall(context.Background(), "claude", twoCopies().env(t))
	if err != nil {
		t.Fatalf("detectInstall: %v", err)
	}

	if len(info.PathEntries) != 2 {
		t.Fatalf("PathEntries = %d entries, want 2: %+v", len(info.PathEntries), info.PathEntries)
	}

	// PATH order is the whole answer to "which one runs".
	first, second := info.PathEntries[0], info.PathEntries[1]
	if first.Path != "/home/u/.local/bin/claude" {
		t.Errorf("first entry = %q, want the winning native copy", first.Path)
	}
	if !first.Active {
		t.Error("first entry is not marked Active")
	}
	if first.Method != InstallNative {
		t.Errorf("first Method = %q, want %q", first.Method, InstallNative)
	}

	if second.Path != "/usr/local/bin/claude" {
		t.Errorf("second entry = %q, want the shadowed npm copy", second.Path)
	}
	if second.Active {
		t.Error("second entry is marked Active; only the winner runs")
	}
	if second.Method != InstallNPMGlobal {
		t.Errorf("second Method = %q, want %q", second.Method, InstallNPMGlobal)
	}
	if second.RealPath != "/usr/local/lib/node_modules/@anthropic-ai/claude-code/cli.js" {
		t.Errorf("second RealPath = %q, want the resolved npm entry point", second.RealPath)
	}

	shadowed := info.Shadowed()
	if len(shadowed) != 1 || shadowed[0].Path != "/usr/local/bin/claude" {
		t.Errorf("Shadowed() = %+v, want just the npm copy", shadowed)
	}

	// This is exactly the case ConfigMismatch cannot see: both copies would be
	// described as native by a config that says native.
	if info.ConfigMismatch {
		t.Error("ConfigMismatch = true; this layout is the one it misses, which is why PathEntries exists")
	}
}

// Whichever copy PATH reaches first is the one that runs, and the entries have
// to say so rather than assuming an order.
func TestPathEntriesFollowPathOrder(t *testing.T) {
	layout := twoCopies()
	layout.dirs = []string{"/usr/local/bin", "/home/u/.local/bin", "/usr/bin"}

	info, err := detectInstall(context.Background(), "claude", layout.env(t))
	if err != nil {
		t.Fatalf("detectInstall: %v", err)
	}
	if info.Method != InstallNPMGlobal {
		t.Errorf("Method = %q, want %q — the npm copy is first on PATH now", info.Method, InstallNPMGlobal)
	}
	if got := info.PathEntries[0].Path; got != "/usr/local/bin/claude" {
		t.Errorf("first entry = %q, want the npm copy", got)
	}
	if !info.PathEntries[0].Active || info.PathEntries[1].Active {
		t.Errorf("Active moved with PATH order incorrectly: %+v", info.PathEntries)
	}
}

func TestPathEntriesSingleCopy(t *testing.T) {
	layout := twoCopies()
	delete(layout.binaries, "/usr/local/bin/claude")

	info, err := detectInstall(context.Background(), "claude", layout.env(t))
	if err != nil {
		t.Fatalf("detectInstall: %v", err)
	}
	if len(info.PathEntries) != 1 {
		t.Fatalf("PathEntries = %+v, want one entry", info.PathEntries)
	}
	if !info.PathEntries[0].Active {
		t.Error("the only copy is not marked Active")
	}
	if len(info.Shadowed()) != 0 {
		t.Errorf("Shadowed() = %+v, want none", info.Shadowed())
	}
}

// Distinct copies, not distinct spellings. A duplicated PATH entry, or two
// directories symlinked together, must not read as a shadowing install.
func TestPathEntriesDeduplicatesTheSameCopy(t *testing.T) {
	layout := pathLayout{
		dirs: []string{"/usr/local/bin", "/bin", "/usr/bin", "/usr/local/bin"},
		binaries: map[string]string{
			// /bin and /usr/bin are the same directory on most distributions.
			"/bin/claude":           "/usr/bin/claude",
			"/usr/bin/claude":       "/usr/bin/claude",
			"/usr/local/bin/claude": "/usr/local/lib/node_modules/@anthropic-ai/claude-code/cli.js",
		},
		files: map[string]string{
			"/usr/bin/claude": elfHeader,
			"/usr/local/lib/node_modules/@anthropic-ai/claude-code/package.json": cliPackageJSON,
		},
	}

	info, err := detectInstall(context.Background(), "claude", layout.env(t))
	if err != nil {
		t.Fatalf("detectInstall: %v", err)
	}
	if len(info.PathEntries) != 2 {
		t.Fatalf("PathEntries = %+v, want 2 distinct copies", info.PathEntries)
	}
	if got := info.PathEntries[1].Path; got != "/bin/claude" {
		t.Errorf("second entry = %q, want the first spelling of the shared copy", got)
	}
	if len(info.Shadowed()) != 1 {
		t.Errorf("Shadowed() = %+v, want one", info.Shadowed())
	}
}

// A copy reached under a second name is still the one that runs.
func TestPathEntriesMarksTheWinnerByResolvedPath(t *testing.T) {
	layout := pathLayout{
		dirs: []string{"/usr/bin"},
		binaries: map[string]string{
			"/usr/bin/claude": "/opt/claude/bin/claude",
			"/bin/claude":     "/opt/claude/bin/claude",
		},
		files: map[string]string{"/opt/claude/bin/claude": elfHeader},
	}
	env := layout.env(t)
	// Detection resolved the winner through /bin, the PATH walk sees /usr/bin.
	env.lookPath = func(name string) (string, error) {
		if filepath.Base(name) == name {
			return "/bin/claude", nil
		}
		if _, ok := layout.binaries[name]; ok {
			return name, nil
		}
		return "", os.ErrNotExist
	}

	info, err := detectInstall(context.Background(), "claude", env)
	if err != nil {
		t.Fatalf("detectInstall: %v", err)
	}
	if len(info.PathEntries) != 1 {
		t.Fatalf("PathEntries = %+v, want one copy under two names", info.PathEntries)
	}
	if !info.PathEntries[0].Active {
		t.Error("the copy that runs is not marked Active when reached under another name")
	}
}

// An explicitly configured binary may sit outside PATH entirely. It still runs.
func TestPathEntriesIncludeAnOffPathBinaryFirst(t *testing.T) {
	layout := twoCopies()
	layout.binaries["/opt/custom/claude"] = "/opt/custom/claude"
	layout.files["/opt/custom/claude"] = elfHeader

	info, err := detectInstall(context.Background(), "/opt/custom/claude", layout.env(t))
	if err != nil {
		t.Fatalf("detectInstall: %v", err)
	}
	if len(info.PathEntries) != 3 {
		t.Fatalf("PathEntries = %+v, want the configured binary plus both PATH copies", info.PathEntries)
	}
	first := info.PathEntries[0]
	if first.Path != "/opt/custom/claude" || !first.Active {
		t.Errorf("first entry = %+v, want the configured binary marked Active", first)
	}
	if len(info.Shadowed()) != 2 {
		t.Errorf("Shadowed() = %+v, want both PATH copies", info.Shadowed())
	}
}

// A machine that cannot enumerate PATH still detects; it just cannot warn.
func TestPathEntriesTolerateNoPathWalk(t *testing.T) {
	env := twoCopies().env(t)
	env.pathDirs = nil

	info, err := detectInstall(context.Background(), "claude", env)
	if err != nil {
		t.Fatalf("detectInstall: %v", err)
	}
	if len(info.PathEntries) != 1 || !info.PathEntries[0].Active {
		t.Fatalf("PathEntries = %+v, want just the winner", info.PathEntries)
	}
}

// A second copy is a warning, never a refusal: it must not change what Update
// or a published-version lookup does.
func TestSecondCopyDoesNotBlockAnUpdate(t *testing.T) {
	layout := twoCopies()
	var run updateRun
	env := updateEnv{
		installEnv: layout.env(t),
		writable:   func(string) error { return nil },
		runUpdate: func(_ context.Context, binary string, _ func(string)) (int, error) {
			run.binary = binary
			return 0, nil
		},
	}
	versions := []string{"2.1.241", "2.1.243"}
	env.runVersion = func(context.Context, string) (string, error) {
		v := versions[0]
		versions = versions[1:]
		return v, nil
	}

	result, err := runUpdate(context.Background(), "claude", env, nil)
	if err != nil {
		t.Fatalf("runUpdate: %v", err)
	}
	if run.binary != "/home/u/.local/bin/claude" {
		t.Errorf("updated %q, want the winning copy", run.binary)
	}
	if !result.Changed {
		t.Error("Changed = false, want the update to have proceeded normally")
	}
}

func TestOSPathDirsMatchesLookPathReading(t *testing.T) {
	t.Setenv("PATH", strings.Join([]string{"/a", "", "/b"}, string(os.PathListSeparator)))
	got := osPathDirs()
	want := []string{"/a", ".", "/b"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("osPathDirs() = %v, want %v", got, want)
	}
}

// The walk has to stay cheap enough for a launch path: it is file reads only,
// with no process spawn per copy.
func BenchmarkFindPathEntries(b *testing.B) {
	env := osInstallEnv()
	found, err := env.lookPath("claude")
	if err != nil {
		b.Skip("no claude on PATH")
	}
	real := found
	if resolved, err := env.evalSymlink(found); err == nil {
		real = resolved
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		findPathEntries("claude", found, real, env)
	}
}
