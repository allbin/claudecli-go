package claudecli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeInstallEnv builds an installEnv backed by an in-memory file map, so
// classification can be exercised over synthetic path layouts without any
// Claude CLI on the machine.
func fakeInstallEnv(files map[string]string) installEnv {
	return installEnv{
		lookPath:    func(s string) (string, error) { return s, nil },
		evalSymlink: func(s string) (string, error) { return s, nil },
		readFile: func(name string) ([]byte, error) {
			// Callers build paths with path.Join, which already cleans them.
			if content, ok := files[name]; ok {
				return []byte(content), nil
			}
			return nil, os.ErrNotExist
		},
		readHeader: func(name string) ([]byte, error) {
			content, ok := files[name]
			if !ok {
				return nil, os.ErrNotExist
			}
			if len(content) > 8 {
				content = content[:8]
			}
			return []byte(content), nil
		},
		runVersion: func(context.Context, string) (string, error) { return "2.1.87", nil },
		configDir:  "/home/u/.claude",
		configFile: "/home/u/.claude.json",
	}
}

const cliPackageJSON = `{"name":"@anthropic-ai/claude-code","version":"2.1.87"}`

// elfHeader is the leading magic of a compiled ELF executable.
const elfHeader = "\x7fELF\x02\x01\x01\x00"

// peHeader is the leading magic of a Windows PE executable.
const peHeader = "MZ\x90\x00\x03\x00\x00\x00"

func TestClassifyInstall(t *testing.T) {
	tests := []struct {
		name     string
		realPath string
		files    map[string]string
		want     classification
	}{
		{
			name:     "npm global with package metadata",
			realPath: "/usr/local/lib/node_modules/@anthropic-ai/claude-code/cli.js",
			files: map[string]string{
				"/usr/local/lib/node_modules/@anthropic-ai/claude-code/package.json": cliPackageJSON,
			},
			want: classification{InstallNPMGlobal, "", "", "", InstallSourcePackageMetadata},
		},
		{
			name:     "npm global without readable metadata falls back to layout",
			realPath: "/usr/lib/node_modules/@anthropic-ai/claude-code/cli.js",
			want:     classification{InstallNPMGlobal, "", "", "", InstallSourcePathLayout},
		},
		{
			name:     "npm global under a custom prefix",
			realPath: "/home/u/.npm-global/lib/node_modules/@anthropic-ai/claude-code/cli.js",
			files: map[string]string{
				"/home/u/.npm-global/lib/node_modules/@anthropic-ai/claude-code/package.json": cliPackageJSON,
			},
			want: classification{InstallNPMGlobal, "", "", "", InstallSourcePackageMetadata},
		},
		{
			name:     "npm global hosted by nvm keeps npm method and names the manager",
			realPath: "/home/u/.nvm/versions/node/v20.11.0/lib/node_modules/@anthropic-ai/claude-code/cli.js",
			files: map[string]string{
				"/home/u/.nvm/versions/node/v20.11.0/lib/node_modules/@anthropic-ai/claude-code/package.json": cliPackageJSON,
			},
			want: classification{InstallNPMGlobal, "nvm", "", "", InstallSourcePackageMetadata},
		},
		{
			name:     "npm global hosted by fnm",
			realPath: "/home/u/.local/share/fnm/node-versions/v22.2.0/installation/lib/node_modules/@anthropic-ai/claude-code/cli.js",
			want:     classification{InstallNPMGlobal, "fnm", "", "", InstallSourcePathLayout},
		},
		{
			name:     "volta tool image",
			realPath: "/home/u/.volta/tools/image/packages/@anthropic-ai/claude-code/lib/node_modules/@anthropic-ai/claude-code/cli.js",
			want:     classification{InstallNPMGlobal, "volta", "", "", InstallSourcePathLayout},
		},
		{
			name:     "npm local install managed by the CLI",
			realPath: "/home/u/.claude/local/node_modules/@anthropic-ai/claude-code/cli.js",
			files: map[string]string{
				"/home/u/.claude/local/node_modules/@anthropic-ai/claude-code/package.json": cliPackageJSON,
			},
			want: classification{InstallNPMLocal, "", "", "", InstallSourcePathLayout},
		},
		{
			name:     "npm local launcher script",
			realPath: "/home/u/.claude/local/claude",
			want:     classification{InstallNPMLocal, "", "", "", InstallSourcePathLayout},
		},
		{
			name:     "native installer versions layout",
			realPath: "/home/u/.local/share/claude/versions/2.1.87",
			files:    map[string]string{"/home/u/.local/share/claude/versions/2.1.87": elfHeader},
			want:     classification{InstallNative, "", "", "", InstallSourcePathLayout},
		},
		{
			name:     "standalone elf binary in an ordinary bin dir",
			realPath: "/opt/claude/bin/claude",
			files:    map[string]string{"/opt/claude/bin/claude": elfHeader},
			want:     classification{InstallNative, "", "", "", InstallSourcePathLayout},
		},
		{
			name:     "standalone windows exe",
			realPath: `C:\Program Files\Claude\claude.exe`,
			files:    map[string]string{"C:/Program Files/Claude/claude.exe": peHeader},
			want:     classification{InstallNative, "", "", "", InstallSourcePathLayout},
		},
		{
			name:     "homebrew cask",
			realPath: "/opt/homebrew/Caskroom/claude-code/2.1.87/claude",
			files:    map[string]string{"/opt/homebrew/Caskroom/claude-code/2.1.87/claude": elfHeader},
			want:     classification{InstallPackageManager, "", "homebrew", "claude-code", InstallSourcePathLayout},
		},
		{
			name:     "homebrew cask keeps a non-default cask name",
			realPath: "/opt/homebrew/Caskroom/claude-code@beta/2.2.0/claude",
			want:     classification{InstallPackageManager, "", "homebrew", "claude-code@beta", InstallSourcePathLayout},
		},
		{
			name:     "winget package",
			realPath: `C:\Users\u\AppData\Local\Microsoft\WinGet\Packages\Anthropic.ClaudeCode_x\claude.exe`,
			want:     classification{InstallPackageManager, "", "winget", "Anthropic.ClaudeCode", InstallSourcePathLayout},
		},
		{
			name:     "mise managed tool",
			realPath: "/home/u/.local/share/mise/installs/claude/2.1.87/bin/claude",
			want:     classification{InstallPackageManager, "mise", "mise", "claude", InstallSourcePathLayout},
		},
		{
			name:     "asdf managed tool",
			realPath: "/home/u/.asdf/installs/claude-code/2.1.87/bin/claude",
			want:     classification{InstallPackageManager, "asdf", "asdf", "claude", InstallSourcePathLayout},
		},
		{
			name:     "version manager root with no packaging evidence",
			realPath: "/home/u/.nvm/versions/node/v20.11.0/bin/claude",
			want:     classification{InstallVersionManager, "nvm", "", "", InstallSourcePathLayout},
		},
		{
			name:     "windows cmd shim beside an npm prefix",
			realPath: `C:\Users\u\AppData\Roaming\npm\claude.cmd`,
			files: map[string]string{
				"C:/Users/u/AppData/Roaming/npm/node_modules/@anthropic-ai/claude-code/package.json": cliPackageJSON,
			},
			want: classification{InstallNPMGlobal, "", "", "", InstallSourcePackageMetadata},
		},
		{
			name:     "unresolvable windows shim is unknown, not native",
			realPath: `C:\Users\u\bin\claude.cmd`,
			files:    map[string]string{"C:/Users/u/bin/claude.cmd": "@echo off\r\n"},
			want:     classification{InstallUnknown, "", "", "", InstallSourceNone},
		},
		{
			name:     "unresolvable powershell shim is unknown",
			realPath: `C:\Users\u\bin\claude.ps1`,
			want:     classification{InstallUnknown, "", "", "", InstallSourceNone},
		},
		{
			name:     "shell wrapper script with no other evidence is unknown",
			realPath: "/home/u/bin/claude",
			files:    map[string]string{"/home/u/bin/claude": "#!/bin/sh\nexec something\n"},
			want:     classification{InstallUnknown, "", "", "", InstallSourceNone},
		},
		{
			name:     "unreadable path with no markers is unknown",
			realPath: "/some/where/claude",
			want:     classification{InstallUnknown, "", "", "", InstallSourceNone},
		},
		{
			name:     "directory merely containing nvm in its name is not a version manager",
			realPath: "/home/u/nvm-notes/claude",
			files:    map[string]string{"/home/u/nvm-notes/claude": elfHeader},
			want:     classification{InstallNative, "", "", "", InstallSourcePathLayout},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyInstall(tt.realPath, fakeInstallEnv(tt.files))
			if got != tt.want {
				t.Errorf("classifyInstall(%q) = %+v, want %+v", tt.realPath, got, tt.want)
			}
		})
	}
}

func TestUpdateCommand(t *testing.T) {
	tests := []struct {
		name           string
		method         InstallMethod
		packageManager string
		packageName    string
		want           string
	}{
		{"native updates itself", InstallNative, "", "", "claude update"},
		{"npm local updates itself", InstallNPMLocal, "", "", "claude update"},
		{"npm global", InstallNPMGlobal, "", "", "npm install -g @anthropic-ai/claude-code@latest"},
		{"homebrew", InstallPackageManager, "homebrew", "claude-code", "brew upgrade claude-code"},
		{"homebrew without a cask name", InstallPackageManager, "homebrew", "", "brew upgrade claude-code"},
		{"winget", InstallPackageManager, "winget", "Anthropic.ClaudeCode", "winget upgrade Anthropic.ClaudeCode"},
		{"mise", InstallPackageManager, "mise", "claude", "mise upgrade claude"},
		{"asdf has no known command", InstallPackageManager, "asdf", "claude", ""},
		{"unrecognized package manager", InstallPackageManager, "pacman", "claude-code", ""},
		{"version manager", InstallVersionManager, "", "", ""},
		{"unknown", InstallUnknown, "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := updateCommand(tt.method, tt.packageManager, tt.packageName); got != tt.want {
				t.Errorf("updateCommand(%q, %q, %q) = %q, want %q",
					tt.method, tt.packageManager, tt.packageName, got, tt.want)
			}
		})
	}
}

// TestUpdateCommandEmptyWhenUnknown locks the rule that makes an unknown
// answer safe: no method without a verified command may emit one.
func TestUpdateCommandEmptyWhenUnknown(t *testing.T) {
	for _, m := range []InstallMethod{InstallUnknown, InstallVersionManager, ""} {
		if got := updateCommand(m, "", ""); got != "" {
			t.Errorf("updateCommand(%q) = %q, want empty — a guessed command half-installs a second copy", m, got)
		}
	}
}

func TestDetectInstall_NotFound(t *testing.T) {
	env := fakeInstallEnv(nil)
	env.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }

	info, err := detectInstall(context.Background(), "claude", env)
	if info != nil {
		t.Errorf("expected nil info, got %+v", info)
	}
	if !errors.Is(err, ErrCLINotFound) {
		t.Fatalf("expected ErrCLINotFound, got %v", err)
	}
	var nf *CLINotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("expected *CLINotFoundError, got %T", err)
	}
	if nf.Binary != "claude" {
		t.Errorf("Binary = %q, want %q", nf.Binary, "claude")
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Error("expected the underlying lookup error to be unwrappable")
	}
}

func TestDetectInstall_ConfigTieBreak(t *testing.T) {
	tests := []struct {
		name          string
		realPath      string
		files         map[string]string
		wantMethod    InstallMethod
		wantSource    InstallSource
		wantConfig    string
		wantMismatch  bool
		wantUpdateCmd string
	}{
		{
			name:          "config resolves an otherwise unknown layout",
			realPath:      "/home/u/bin/claude",
			files:         map[string]string{"/home/u/.claude.json": `{"installMethod":"native"}`},
			wantMethod:    InstallNative,
			wantSource:    InstallSourceConfig,
			wantConfig:    "native",
			wantUpdateCmd: "claude update",
		},
		{
			name:     "path evidence outranks a stale config and flags the mismatch",
			realPath: "/usr/local/lib/node_modules/@anthropic-ai/claude-code/cli.js",
			files: map[string]string{
				"/usr/local/lib/node_modules/@anthropic-ai/claude-code/package.json": cliPackageJSON,
				"/home/u/.claude.json": `{"installMethod":"native"}`,
			},
			wantMethod:    InstallNPMGlobal,
			wantSource:    InstallSourcePackageMetadata,
			wantConfig:    "native",
			wantMismatch:  true,
			wantUpdateCmd: "npm install -g @anthropic-ai/claude-code@latest",
		},
		{
			name:     "package-manager installs never flag a mismatch the config cannot express",
			realPath: "/opt/homebrew/Caskroom/claude-code/2.1.87/claude",
			files: map[string]string{
				"/home/u/.claude.json": `{"installMethod":"native"}`,
			},
			wantMethod:    InstallPackageManager,
			wantSource:    InstallSourcePathLayout,
			wantConfig:    "native",
			wantUpdateCmd: "brew upgrade claude-code",
		},
		{
			name:       "unreadable config leaves an unknown layout unknown",
			realPath:   "/home/u/bin/claude",
			wantMethod: InstallUnknown,
			wantSource: InstallSourceNone,
		},
		{
			name:       "unrecognized config value is reported verbatim but not trusted",
			realPath:   "/home/u/bin/claude",
			files:      map[string]string{"/home/u/.claude.json": `{"installMethod":"snap"}`},
			wantMethod: InstallUnknown,
			wantSource: InstallSourceNone,
			wantConfig: "snap",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := fakeInstallEnv(tt.files)
			env.lookPath = func(string) (string, error) { return tt.realPath, nil }

			info, err := detectInstall(context.Background(), "claude", env)
			if err != nil {
				t.Fatalf("detectInstall: %v", err)
			}
			if info.Method != tt.wantMethod {
				t.Errorf("Method = %q, want %q", info.Method, tt.wantMethod)
			}
			if info.Source != tt.wantSource {
				t.Errorf("Source = %q, want %q", info.Source, tt.wantSource)
			}
			if info.ConfigMethod != tt.wantConfig {
				t.Errorf("ConfigMethod = %q, want %q", info.ConfigMethod, tt.wantConfig)
			}
			if info.ConfigMismatch != tt.wantMismatch {
				t.Errorf("ConfigMismatch = %v, want %v", info.ConfigMismatch, tt.wantMismatch)
			}
			if info.UpdateCmd != tt.wantUpdateCmd {
				t.Errorf("UpdateCmd = %q, want %q", info.UpdateCmd, tt.wantUpdateCmd)
			}
			if info.Version != "2.1.87" {
				t.Errorf("Version = %q, want %q", info.Version, "2.1.87")
			}
		})
	}
}

// TestDetectInstall_VersionProbeFailureIsNotFatal locks that a binary we cannot
// run still yields a usable classification.
func TestDetectInstall_VersionProbeFailureIsNotFatal(t *testing.T) {
	env := fakeInstallEnv(map[string]string{
		"/usr/local/lib/node_modules/@anthropic-ai/claude-code/package.json": cliPackageJSON,
	})
	env.lookPath = func(string) (string, error) {
		return "/usr/local/lib/node_modules/@anthropic-ai/claude-code/cli.js", nil
	}
	env.runVersion = func(context.Context, string) (string, error) {
		return "", errors.New("exec format error")
	}

	info, err := detectInstall(context.Background(), "claude", env)
	if err != nil {
		t.Fatalf("detectInstall: %v", err)
	}
	if info.Version != "" {
		t.Errorf("Version = %q, want empty", info.Version)
	}
	if info.Method != InstallNPMGlobal {
		t.Errorf("Method = %q, want %q", info.Method, InstallNPMGlobal)
	}
}

// TestDetectInstall_SymlinkChain walks a real symlink chain through a
// version-manager-shaped directory: only the resolved path reveals that the
// shim on PATH is an npm global hosted by fnm.
func TestDetectInstall_SymlinkChain(t *testing.T) {
	root := t.TempDir()

	pkgDir := filepath.Join(root, ".local", "share", "fnm", "node-versions", "v22.2.0",
		"installation", "lib", "node_modules", "@anthropic-ai", "claude-code")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cliJS := filepath.Join(pkgDir, "cli.js")
	writeFile(t, cliJS, "#!/usr/bin/env node\n")
	writeFile(t, filepath.Join(pkgDir, "package.json"), cliPackageJSON)

	// <node>/bin/claude -> ../lib/node_modules/@anthropic-ai/claude-code/cli.js
	nodeBin := filepath.Join(root, ".local", "share", "fnm", "node-versions", "v22.2.0", "installation", "bin")
	if err := os.MkdirAll(nodeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	nodeShim := filepath.Join(nodeBin, "claude")
	symlinkOrSkip(t, cliJS, nodeShim)

	// ~/.local/bin/claude -> the fnm shim (what a user actually has on PATH).
	pathDir := filepath.Join(root, "pathbin")
	if err := os.MkdirAll(pathDir, 0o755); err != nil {
		t.Fatal(err)
	}
	onPath := filepath.Join(pathDir, "claude")
	symlinkOrSkip(t, nodeShim, onPath)

	env := osInstallEnv()
	env.configDir = filepath.Join(root, ".claude")
	env.configFile = filepath.Join(root, ".claude.json")
	env.lookPath = func(string) (string, error) { return onPath, nil }
	env.runVersion = func(context.Context, string) (string, error) { return "2.1.87", nil }

	info, err := detectInstall(context.Background(), "claude", env)
	if err != nil {
		t.Fatalf("detectInstall: %v", err)
	}
	if info.Path != onPath {
		t.Errorf("Path = %q, want %q", info.Path, onPath)
	}
	// EvalSymlinks also resolves the temp dir itself (macOS /var -> /private/var),
	// so compare on the tail that identifies the package.
	if !strings.HasSuffix(normalizeInstallPath(info.RealPath),
		"/node_modules/@anthropic-ai/claude-code/cli.js") {
		t.Errorf("RealPath = %q, want it resolved into the npm package", info.RealPath)
	}
	if info.RealPath == info.Path {
		t.Error("RealPath must differ from Path — the shim was not resolved")
	}
	if info.Method != InstallNPMGlobal {
		t.Errorf("Method = %q, want %q", info.Method, InstallNPMGlobal)
	}
	if info.Source != InstallSourcePackageMetadata {
		t.Errorf("Source = %q, want %q", info.Source, InstallSourcePackageMetadata)
	}
	if info.VersionManager != "fnm" {
		t.Errorf("VersionManager = %q, want %q", info.VersionManager, "fnm")
	}
	if info.UpdateCmd != "npm install -g @anthropic-ai/claude-code@latest" {
		t.Errorf("UpdateCmd = %q", info.UpdateCmd)
	}
}

// TestDetectInstall_SymlinkChainToNative covers the other real-world chain:
// a bin-dir symlink into the native installer's versioned binary.
func TestDetectInstall_SymlinkChainToNative(t *testing.T) {
	root := t.TempDir()

	versionsDir := filepath.Join(root, ".local", "share", "claude", "versions")
	if err := os.MkdirAll(versionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(versionsDir, "2.1.87")
	writeFile(t, binary, elfHeader+"rest of the binary")

	pathDir := filepath.Join(root, ".local", "bin")
	if err := os.MkdirAll(pathDir, 0o755); err != nil {
		t.Fatal(err)
	}
	onPath := filepath.Join(pathDir, "claude")
	symlinkOrSkip(t, binary, onPath)

	env := osInstallEnv()
	env.configDir = filepath.Join(root, ".claude")
	env.configFile = filepath.Join(root, ".claude.json")
	env.lookPath = func(string) (string, error) { return onPath, nil }
	env.runVersion = func(context.Context, string) (string, error) { return "2.1.87", nil }

	info, err := detectInstall(context.Background(), "claude", env)
	if err != nil {
		t.Fatalf("detectInstall: %v", err)
	}
	if info.Method != InstallNative {
		t.Errorf("Method = %q, want %q", info.Method, InstallNative)
	}
	if info.UpdateCmd != "claude update" {
		t.Errorf("UpdateCmd = %q, want %q", info.UpdateCmd, "claude update")
	}
	if info.VersionManager != "" {
		t.Errorf("VersionManager = %q, want empty", info.VersionManager)
	}
}

func TestParseVersionOutput(t *testing.T) {
	tests := []struct{ in, want string }{
		{"2.1.87 (Claude Code)\n", "2.1.87"},
		{"2.2.0-beta.1 (Claude Code)\n", "2.2.0-beta.1"},
		{"v2.1.87\n", "2.1.87"},
		{"  2.1.87  ", "2.1.87"},
		{"", ""},
		{"\n", ""},
		{"error: something went wrong", ""},
	}
	for _, tt := range tests {
		if got := parseVersionOutput(tt.in); got != tt.want {
			t.Errorf("parseVersionOutput(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestMethodFromConfig(t *testing.T) {
	tests := []struct {
		in   string
		want InstallMethod
	}{
		{"native", InstallNative},
		{"global", InstallNPMGlobal},
		{"local", InstallNPMLocal},
		{"", InstallUnknown},
		{"homebrew", InstallUnknown},
	}
	for _, tt := range tests {
		if got := methodFromConfig(tt.in); got != tt.want {
			t.Errorf("methodFromConfig(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// symlinkOrSkip creates a symlink, skipping the test where the platform does
// not permit it (unprivileged Windows).
func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatal(err)
	}
}
