package claudecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// CLIPackageName is the npm package that publishes the Claude CLI. It is the
// package a consumer would query for the latest published version, and the one
// named by the npm update command in InstallInfo.UpdateCmd.
const CLIPackageName = "@anthropic-ai/claude-code"

// InstallMethod describes how the Claude CLI on PATH was installed.
//
// The zero value is the empty string, which is not a valid method — use
// InstallUnknown for "could not be determined".
type InstallMethod string

const (
	// InstallNPMGlobal is a global npm/pnpm/yarn install: the resolved binary
	// lives under a `node_modules/@anthropic-ai/claude-code` tree owned by a
	// package manager prefix.
	InstallNPMGlobal InstallMethod = "npm-global"

	// InstallNPMLocal is the CLI's own per-user "local" install under the
	// Claude config directory (`~/.claude/local`), which the CLI manages
	// itself.
	InstallNPMLocal InstallMethod = "npm-local"

	// InstallVersionManager is a binary living inside a tool-version-manager
	// root (fnm, nvm, volta, asdf, mise) with no package metadata explaining
	// how it got there. The version manager owns the directory, so no generic
	// update command is safe to suggest.
	InstallVersionManager InstallMethod = "version-manager"

	// InstallPackageManager is an OS package manager install (Homebrew cask,
	// winget, mise, asdf). See InstallInfo.PackageManager for which one.
	InstallPackageManager InstallMethod = "package-manager"

	// InstallNative is a standalone binary — the native installer's versioned
	// layout, or an executable in an ordinary bin directory that no package
	// manager claims. Native installs update themselves via `claude update`.
	InstallNative InstallMethod = "native"

	// InstallUnknown means detection found no conclusive evidence. This is a
	// legitimate, useful answer: InstallInfo.UpdateCmd is empty and the caller
	// should tell the user to update manually rather than run a guess.
	InstallUnknown InstallMethod = "unknown"
)

// InstallSource records which evidence produced InstallInfo.Method, so a
// caller can weigh how much to trust it.
type InstallSource string

const (
	// InstallSourcePackageMetadata means a package.json belonging to
	// CLIPackageName was found alongside the resolved binary. This is the
	// strongest signal: the packaging metadata describes the binary that
	// actually runs.
	InstallSourcePackageMetadata InstallSource = "package-metadata"

	// InstallSourcePathLayout means the resolved path matched a known install
	// layout (a node_modules tree, a Homebrew Caskroom, a WinGet package dir,
	// the native installer's versions directory, an executable file header).
	InstallSourcePathLayout InstallSource = "path-layout"

	// InstallSourceConfig means path evidence was inconclusive and the method
	// came from `installMethod` in the CLI's own config file. The config
	// records how the CLI was last *installed*, which need not describe the
	// binary currently first on PATH — treat it as weaker than the other two.
	InstallSourceConfig InstallSource = "config"

	// InstallSourceNone means nothing conclusive was found.
	InstallSourceNone InstallSource = "none"
)

// InstallInfo describes the Claude CLI installation currently first on PATH.
type InstallInfo struct {
	// Path is the binary as found on PATH, before symlinks are resolved.
	Path string

	// RealPath is Path with all symlinks resolved. Classification is driven by
	// this, not Path: a version-manager or npm shim only reveals what it is
	// once resolved.
	RealPath string

	// Version is what the CLI reports for itself (e.g. "2.1.87"), or "" if the
	// version probe failed. Detection does not fail just because the version
	// could not be read.
	Version string

	// Method is how the binary at RealPath was installed.
	Method InstallMethod

	// UpdateCmd is the command to show the user, or "" when no command is
	// known to be correct. Never treat "" as "use npm" — see the package
	// doc on DetectInstall for why guessing is worse than saying nothing.
	UpdateCmd string

	// VersionManager names the tool version manager whose directory the binary
	// lives under ("fnm", "nvm", "volta", "asdf", "mise"), or "" if none. It is
	// set even when Method is InstallNPMGlobal: a global npm install hosted by
	// a node version manager only updates for the node version currently
	// active, which is worth telling the user.
	VersionManager string

	// PackageManager names the OS package manager ("homebrew", "winget",
	// "mise", "asdf") when Method is InstallPackageManager, else "".
	PackageManager string

	// PackageName is the identifier that package manager knows this install by
	// (a Homebrew cask name, a winget package id), or "" when not applicable.
	PackageName string

	// ConfigMethod is the raw `installMethod` value from the CLI's config file
	// ("native", "global", "local"), or "" when unset or unreadable. It is
	// reported verbatim and is not normalized into Method's vocabulary.
	ConfigMethod string

	// ConfigMismatch is true when ConfigMethod disagrees with the detected
	// Method. That usually means a second copy was installed by a different
	// route and now shadows the first on PATH — the exact situation that makes
	// a wrong update command destructive. Only set when both values are known
	// and comparable; the config file cannot express package-manager or
	// version-manager installs, so those never flag a mismatch.
	//
	// It is a weak detector of a second copy and must not be used as one: it
	// needs the two copies to disagree about method *and* the config to record
	// the loser. Two copies the config agrees with leave it false. Use
	// PathEntries to see the copies themselves.
	ConfigMismatch bool

	// PathEntries is every copy of the CLI found on PATH, in PATH order, with
	// the one that runs marked Active. See [InstallPathEntry] and
	// [InstallInfo.Shadowed].
	//
	// More than one entry is a normal state that deserves a warning, never a
	// refusal: nothing in this package fails or changes behaviour because of
	// it. The shadowed copy does nothing at all until PATH changes — and then
	// it does everything, which is the point.
	PathEntries []InstallPathEntry

	// Source records which evidence produced Method.
	Source InstallSource

	// AutoUpdate describes the CLI's own background updater for this install:
	// whether it is enabled, which release channel it tracks, and how its last
	// attempt went. Never nil. See [AutoUpdateState] — knowing an install
	// updates itself is what lets a consumer stay quiet about a version that
	// is already being handled.
	AutoUpdate *AutoUpdateState
}

// ErrCLINotFound matches the error returned by DetectInstall when no Claude
// CLI is on PATH. Use errors.Is to distinguish "no CLI installed" — a normal
// state for a consumer probing the environment — from a real failure.
var ErrCLINotFound = errors.New("claude CLI not found on PATH")

// CLINotFoundError is returned when the CLI binary cannot be resolved on PATH.
// Use errors.As to read which binary name was looked up.
type CLINotFoundError struct {
	Binary string // the name or path that was looked up
	Err    error  // the underlying exec.LookPath error
}

func (e *CLINotFoundError) Error() string {
	return fmt.Sprintf("claudecli: %q not found on PATH", e.Binary)
}

func (e *CLINotFoundError) Is(target error) bool {
	return target == ErrCLINotFound
}

func (e *CLINotFoundError) Unwrap() error { return e.Err }

// defaultInstallTimeout bounds the `claude -v` probe when the caller's context
// has no deadline.
const defaultInstallTimeout = 5 * time.Second

// DetectInstall reports how the Claude CLI on PATH was installed and which
// command updates it, using the default client's binary.
//
// # Detect, never assume
//
// The update command must be derived from evidence about the binary that
// actually runs, never from a default. Suggesting the wrong one does not fail
// cleanly: `npm install -g` against a native install writes a second, complete
// copy into an npm prefix, and whichever copy PATH happens to reach first from
// then on is the one that answers `claude --version`. The user is now told a
// version that does not describe the binary their next session will run, and
// the copy they actually use is still stale. Because that failure is silent,
// InstallUnknown with an empty UpdateCmd is the correct answer whenever the
// evidence is inconclusive — "update manually" costs the user a web search,
// a wrong command costs them a broken install they cannot see.
//
// # What it does
//
// Detection is read-only: it resolves the binary with exec.LookPath and
// filepath.EvalSymlinks, reads package metadata and file headers next to the
// resolved path, reads the CLI's config file, its settings file and its
// last-update record, and runs `claude -v`. It starts no session, writes
// nothing, and makes no network calls. Notably it does not shell out to
// `claude doctor`, which reports the same facts but rewrites the config file
// and probes the network to do it.
//
// This reports only what is installed locally. What is *published* needs the
// network and lives in [LatestPublished], which is deliberately a separate call
// so this one stays cheap enough for a launch path.
//
// # Two copies is a normal state
//
// PathEntries lists every copy on PATH, in PATH order, so a caller can warn
// about a second one. That is a real state, not a hypothetical: a native
// install in ~/.local/bin winning over a fifteen-month-old npm-global copy in
// /usr/local/bin was measured on one ordinary machine. ConfigMismatch does not
// see it — the config said native, detection said native, they agreed — while
// one PATH change would silently downgrade the running CLI by fifteen months
// and the reported version would go on describing the copy that no longer runs.
//
// It is a warning and never a refusal. Nothing here fails, and neither [Update]
// nor [LatestPublished] changes behaviour, because of a second copy.
//
// # Precedence
//
// Package metadata beats path layout, and path layout beats the config file.
// The `installMethod` recorded in the CLI's config says how the CLI was last
// installed, which need not describe the binary now first on PATH, so it is
// only used when path evidence is inconclusive (Source is then
// InstallSourceConfig). When it disagrees with conclusive path evidence the
// path wins and ConfigMismatch is set.
//
// # Windows
//
// Windows classification has not been verified on real hardware. `.exe`
// binaries and npm's `node_modules` layout are handled, but a `.cmd`, `.bat`,
// or `.ps1` shim is not a symlink and cannot be resolved further, so unless a
// sibling npm layout confirms the install it is reported as InstallUnknown
// rather than guessed at.
//
// A missing CLI is not a failure: the returned error satisfies
// errors.Is(err, ErrCLINotFound) and carries no other meaning.
func DetectInstall(ctx context.Context) (*InstallInfo, error) {
	return defaultClient.DetectInstall(ctx)
}

// DetectInstall reports how this client's Claude CLI was installed. See the
// package-level [DetectInstall] for the full contract.
func (c *Client) DetectInstall(ctx context.Context) (*InstallInfo, error) {
	info, err := detectInstall(ctx, c.binaryPath(), osInstallEnv())
	if err != nil {
		return nil, err
	}
	c.log().Debug("detect install",
		"path", info.Path, "realPath", info.RealPath, "version", info.Version,
		"method", info.Method, "source", info.Source, "updateCmd", info.UpdateCmd,
		"versionManager", info.VersionManager, "packageManager", info.PackageManager,
		"configMethod", info.ConfigMethod, "configMismatch", info.ConfigMismatch,
		"pathCopies", len(info.PathEntries), "shadowed", len(info.Shadowed()))
	return info, nil
}

// installEnv is the set of ambient lookups DetectInstall performs, injectable
// so tests can drive classification over synthetic layouts.
type installEnv struct {
	lookPath    func(string) (string, error)
	evalSymlink func(string) (string, error)
	readFile    func(string) ([]byte, error) // small files: package.json, .claude.json
	readHeader  func(string) ([]byte, error) // first bytes of a possibly huge binary
	runVersion  func(ctx context.Context, binary string) (string, error)
	pathDirs    func() []string     // PATH entries in order; nil skips the shadow walk
	getenv      func(string) string // nil means os.Getenv; see installEnv.env
	configDir   string              // $CLAUDE_CONFIG_DIR, else ~/.claude
	configFile  string              // $CLAUDE_CONFIG_DIR/.claude.json, else ~/.claude.json
	dataDir     string              // $XDG_DATA_HOME, else ~/.local/share
}

func osInstallEnv() installEnv {
	dir, file := claudeConfigPaths()
	return installEnv{
		lookPath:    exec.LookPath,
		evalSymlink: filepath.EvalSymlinks,
		readFile:    readSmallFile,
		readHeader:  readFileHeader,
		runVersion:  runVersionProbe,
		pathDirs:    osPathDirs,
		getenv:      os.Getenv,
		configDir:   dir,
		configFile:  file,
		dataDir:     xdgDataDir(),
	}
}

// env reads an environment variable, tolerating a zero-valued installEnv so a
// test fixture only has to populate the fields its case actually exercises.
func (e installEnv) env(name string) string {
	if e.getenv == nil {
		return ""
	}
	return e.getenv(name)
}

// xdgDataDir reports the base directory the native installer keeps its
// versioned binaries under — $XDG_DATA_HOME, else ~/.local/share, matching the
// CLI's own resolution.
func xdgDataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share")
}

// claudeConfigPaths reports the CLI's config directory and config file.
// CLAUDE_CONFIG_DIR relocates both; otherwise the directory is ~/.claude and
// the file is ~/.claude.json (they are deliberately not nested).
func claudeConfigPaths() (dir, file string) {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d, filepath.Join(d, ".claude.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	return filepath.Join(home, ".claude"), filepath.Join(home, ".claude.json")
}

// maxConfigFileSize caps reads of package.json and .claude.json so a
// pathological file cannot balloon memory during a probe.
const maxConfigFileSize = 16 << 20

func readSmallFile(name string) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxConfigFileSize))
}

func readFileHeader(name string) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, 8)
	n, err := io.ReadFull(f, buf)
	if n == 0 {
		return nil, err
	}
	return buf[:n], nil
}

// runVersionProbe runs `<binary> -v` and returns the version token the CLI
// prints (it emits e.g. "2.1.87 (Claude Code)").
func runVersionProbe(ctx context.Context, binary string) (string, error) {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultInstallTimeout)
		defer cancel()
	}
	out, err := exec.CommandContext(ctx, binary, "-v").Output()
	if err != nil {
		return "", err
	}
	return parseVersionOutput(string(out)), nil
}

// parseVersionOutput extracts the version token from `claude -v` output,
// preserving any prerelease suffix the CLI reports.
func parseVersionOutput(out string) string {
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return ""
	}
	v := strings.TrimPrefix(fields[0], "v")
	if v == "" || v[0] < '0' || v[0] > '9' {
		return ""
	}
	return v
}

func detectInstall(ctx context.Context, binary string, env installEnv) (*InstallInfo, error) {
	if binary == "" {
		binary = "claude"
	}
	found, err := env.lookPath(binary)
	if err != nil {
		return nil, &CLINotFoundError{Binary: binary, Err: err}
	}

	real := found
	if resolved, err := env.evalSymlink(found); err == nil && resolved != "" {
		real = resolved
	}

	cls := classifyInstall(real, env)
	cfg := readClaudeConfig(env)
	info := &InstallInfo{
		Path:           found,
		RealPath:       real,
		Method:         cls.method,
		VersionManager: cls.versionManager,
		PackageManager: cls.packageManager,
		PackageName:    cls.packageName,
		Source:         cls.source,
		ConfigMethod:   cfg.InstallMethod,
	}

	// The config file is the CLI's own record of how it was last installed. It
	// only breaks ties: it may describe a different copy than the one on PATH.
	if configMethod := methodFromConfig(info.ConfigMethod); configMethod != InstallUnknown {
		switch {
		case info.Method == InstallUnknown:
			info.Method = configMethod
			info.Source = InstallSourceConfig
		case info.Method != configMethod && configMethodComparable(info.Method):
			info.ConfigMismatch = true
		}
	}

	info.UpdateCmd = updateCommand(info.Method, info.PackageManager, info.PackageName)
	info.AutoUpdate = readAutoUpdateState(env, info, cfg)
	info.PathEntries = findPathEntries(binary, found, real, env)
	info.Version, _ = env.runVersion(ctx, found)
	return info, nil
}

type classification struct {
	method         InstallMethod
	versionManager string
	packageManager string
	packageName    string
	source         InstallSource
}

// classifyInstall derives the install method from a fully symlink-resolved
// path. The order matters and mirrors the CLI's own detection: the npm layouts
// are the most specific, then OS package managers, then version-manager roots,
// and only then the standalone-binary fallback.
func classifyInstall(realPath string, env installEnv) classification {
	p := normalizeInstallPath(realPath)
	segs := strings.Split(p, "/")
	vm := detectVersionManager(segs)

	if isNPMLocal(p, env.configDir) {
		return classification{InstallNPMLocal, vm, "", "", InstallSourcePathLayout}
	}

	if src, ok := npmEvidence(p, env); ok {
		return classification{InstallNPMGlobal, vm, "", "", src}
	}

	if pm, name, ok := detectOSPackageManager(p, segs); ok {
		return classification{InstallPackageManager, vm, pm, name, InstallSourcePathLayout}
	}

	// A version-manager root with no package metadata explaining how the binary
	// got there: name the manager, but do not invent an update command.
	if vm != "" {
		return classification{InstallVersionManager, vm, "", "", InstallSourcePathLayout}
	}

	if isNativeVersionsLayout(segs) || isNativeExecutable(p, env) {
		return classification{InstallNative, "", "", "", InstallSourcePathLayout}
	}

	return classification{InstallUnknown, vm, "", "", InstallSourceNone}
}

// normalizeInstallPath converts a path to forward slashes so a single set of
// segment rules covers both Windows and Unix layouts.
func normalizeInstallPath(p string) string {
	return strings.ReplaceAll(filepath.ToSlash(p), `\`, "/")
}

// isNPMLocal reports whether the binary lives in the CLI's own per-user local
// install root. The CLI hardcodes the `.claude/local` shape, so both the
// literal segment and a relocated config directory are matched.
func isNPMLocal(p, configDir string) bool {
	if strings.Contains(p, "/.claude/local/") {
		return true
	}
	if configDir == "" {
		return false
	}
	root := strings.TrimSuffix(normalizeInstallPath(configDir), "/") + "/local/"
	return strings.HasPrefix(p, root)
}

// npmEvidence reports whether the resolved path belongs to an npm-installed
// copy of the CLI, strongest evidence first.
func npmEvidence(p string, env installEnv) (InstallSource, bool) {
	// The packaging metadata describing this very binary.
	dir := path.Dir(p)
	for i := 0; i < 6; i++ {
		if isCLIPackageJSON(path.Join(dir, "package.json"), env) {
			return InstallSourcePackageMetadata, true
		}
		parent := path.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	// Windows shims are plain text files, not symlinks, so EvalSymlinks cannot
	// reach the package. npm writes them beside the prefix's node_modules tree.
	if isWindowsShim(p) {
		shimDir := path.Dir(p)
		for _, rel := range []string{
			path.Join("node_modules", CLIPackageName, "package.json"),
			path.Join("..", "lib", "node_modules", CLIPackageName, "package.json"),
		} {
			if isCLIPackageJSON(path.Join(shimDir, rel), env) {
				return InstallSourcePackageMetadata, true
			}
		}
		// An unresolvable shim with nothing to confirm it: say so, do not guess.
		return "", false
	}

	if strings.Contains(p, "/node_modules/"+CLIPackageName+"/") ||
		strings.Contains(p, "/node_modules/@anthropic-ai/") {
		return InstallSourcePathLayout, true
	}
	return "", false
}

func isCLIPackageJSON(file string, env installEnv) bool {
	b, err := env.readFile(file)
	if err != nil {
		return false
	}
	var pkg struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(b, &pkg) != nil {
		return false
	}
	return pkg.Name == CLIPackageName
}

func isWindowsShim(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".cmd", ".bat", ".ps1":
		return true
	}
	return false
}

// versionManagerSegments maps a path segment to the tool version manager that
// owns it. Matching is segment-exact so an unrelated directory whose name
// merely contains "nvm" cannot trigger it.
var versionManagerSegments = map[string]string{
	".nvm": "nvm", "nvm": "nvm",
	".fnm": "fnm", "fnm": "fnm",
	".volta": "volta", "volta": "volta",
	".asdf": "asdf", "asdf": "asdf",
	".mise": "mise", "mise": "mise",
}

func detectVersionManager(segs []string) string {
	for _, s := range segs {
		if name, ok := versionManagerSegments[s]; ok {
			return name
		}
	}
	return ""
}

// detectOSPackageManager mirrors the CLI's own package-manager detection,
// which keys entirely off where the running binary lives.
func detectOSPackageManager(p string, segs []string) (manager, name string, ok bool) {
	for i, s := range segs {
		if !strings.EqualFold(s, "Caskroom") {
			continue
		}
		cask := "claude-code"
		if i+1 < len(segs) && segs[i+1] != "" {
			cask = segs[i+1]
		}
		return "homebrew", cask, true
	}

	lower := strings.ToLower(p)
	if strings.Contains(lower, "/microsoft/winget/packages/") ||
		strings.Contains(lower, "/microsoft/winget/links/") {
		return "winget", "Anthropic.ClaudeCode", true
	}
	// Both managers keep tools under `<root>/installs/`, and both roots appear
	// dotted or undotted depending on how they were set up.
	for _, m := range []string{"mise", "asdf"} {
		if strings.Contains(lower, "/"+m+"/installs/") || strings.Contains(lower, "/."+m+"/installs/") {
			return m, "claude", true
		}
	}
	return "", "", false
}

// isNativeVersionsLayout matches the native installer's `<data>/claude/versions/<v>`
// layout, where the binary is the version directory entry itself.
func isNativeVersionsLayout(segs []string) bool {
	for i := 0; i+1 < len(segs); i++ {
		if segs[i] == "claude" && segs[i+1] == "versions" {
			return true
		}
	}
	return false
}

// isNativeExecutable reports whether the resolved file is a compiled
// executable rather than a script. Reached only after the npm, package-manager
// and version-manager layouts have been ruled out, so a standalone binary in
// an ordinary bin directory is what remains.
func isNativeExecutable(p string, env installEnv) bool {
	hdr, err := env.readHeader(p)
	if err != nil {
		return false
	}
	for _, magic := range [][]byte{
		[]byte("\x7fELF"),        // ELF (Linux, BSD)
		{0xcf, 0xfa, 0xed, 0xfe}, // Mach-O 64-bit
		{0xce, 0xfa, 0xed, 0xfe}, // Mach-O 32-bit
		{0xca, 0xfe, 0xba, 0xbe}, // Mach-O universal
		{0xbe, 0xba, 0xfe, 0xca}, // Mach-O universal, byte-swapped
		[]byte("MZ"),             // Windows PE
	} {
		if bytes.HasPrefix(hdr, magic) {
			return true
		}
	}
	return false
}

// claudeConfig is the slice of the CLI's config file this package reads. It is
// deliberately read once per detection: the file also holds per-project history
// and can run to tens of kilobytes.
type claudeConfig struct {
	// InstallMethod is the raw `installMethod` value ("native", "global",
	// "local"), or "" when unset.
	InstallMethod string `json:"installMethod"`

	// AutoUpdates is the CLI's own background-updater preference. A pointer
	// because the key is usually absent, and absent means enabled — the CLI
	// only writes it when the user turns updates off.
	AutoUpdates *bool `json:"autoUpdates"`
}

// readClaudeConfig reads the CLI's config file. Any failure is reported as a
// zero config — it is corroboration, never a prerequisite.
func readClaudeConfig(env installEnv) claudeConfig {
	var cfg claudeConfig
	if env.configFile == "" {
		return cfg
	}
	b, err := env.readFile(env.configFile)
	if err != nil {
		return cfg
	}
	if json.Unmarshal(b, &cfg) != nil {
		return claudeConfig{}
	}
	return cfg
}

// methodFromConfig maps the CLI's config vocabulary onto InstallMethod. The
// config file has no value for package-manager or version-manager installs.
func methodFromConfig(v string) InstallMethod {
	switch v {
	case "native":
		return InstallNative
	case "global":
		return InstallNPMGlobal
	case "local":
		return InstallNPMLocal
	default:
		return InstallUnknown
	}
}

// configMethodComparable reports whether a detected method is one the config
// file is able to express, so a mismatch means disagreement rather than a gap
// in the config's vocabulary.
func configMethodComparable(m InstallMethod) bool {
	switch m {
	case InstallNative, InstallNPMGlobal, InstallNPMLocal:
		return true
	default:
		return false
	}
}

// updateCommand returns the command that updates this install, or "" when none
// is known to be correct. The npm, Homebrew, winget and mise commands match
// what the CLI itself runs or prints for those installs.
func updateCommand(m InstallMethod, packageManager, packageName string) string {
	switch m {
	case InstallNative, InstallNPMLocal:
		// Both are managed by the CLI's own updater.
		return "claude update"
	case InstallNPMGlobal:
		return "npm install -g " + CLIPackageName + "@latest"
	case InstallPackageManager:
		switch packageManager {
		case "homebrew":
			if packageName == "" {
				packageName = "claude-code"
			}
			return "brew upgrade " + packageName
		case "winget":
			return "winget upgrade Anthropic.ClaudeCode"
		case "mise":
			return "mise upgrade claude"
		}
		// asdf and anything else: the CLI does not know a command here either.
		return ""
	default:
		return ""
	}
}
