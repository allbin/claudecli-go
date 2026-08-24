package claudecli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// defaultUpdateTimeout bounds `claude update` when the caller's context carries
// no deadline. A native update downloads a several-hundred-megabyte binary, so
// the bound is generous on purpose: it exists to stop a wedged updater hanging
// a consumer forever, not to cut a slow network short.
const defaultUpdateTimeout = 10 * time.Minute

// updateInterruptGrace is how long a cancelled update is given to unwind after
// its interrupt signal before the process is killed outright. The CLI stages a
// download to a temporary path and renames it into place, so letting it notice
// the signal is what keeps a cancelled update from leaving a partial file where
// a later DetectInstall would find it.
const updateInterruptGrace = 5 * time.Second

// maxUpdateOutputLines caps the transcript kept in UpdateResult.Output. The
// updater is chatty about progress; the tail is what explains a failure.
const maxUpdateOutputLines = 200

// ErrManualUpdate matches the error returned by [Update] when the install is
// not one the CLI's own updater manages. Use errors.Is to distinguish it: this
// is a normal outcome, not a failure. It is the answer for most installs in the
// wild, and the right response is to show the user a command, not an error.
var ErrManualUpdate = errors.New("claudecli: install is not managed by the CLI's own updater")

// ManualUpdateError is returned when the detected install must be updated by
// something other than the CLI. Use errors.As to read the command to display.
type ManualUpdateError struct {
	// Method is the detected install method that cannot self-update.
	Method InstallMethod

	// Command is what the user has to run, verbatim and ready to display — or
	// "" when no command is known to be correct. Never substitute a guess for
	// an empty Command: see [DetectInstall] on why a wrong update command
	// fails silently and expensively.
	Command string
}

func (e *ManualUpdateError) Error() string {
	if e.Command == "" {
		return fmt.Sprintf("claudecli: %s install must be updated manually; no update command is known to be correct", e.Method)
	}
	return fmt.Sprintf("claudecli: %s install must be updated manually with %q", e.Method, e.Command)
}

func (e *ManualUpdateError) Is(target error) bool { return target == ErrManualUpdate }

// ErrUpdateNotWritable matches the error returned by [Update] when the install
// is self-managed but the directory its updater writes into cannot be written.
//
// Keep this distinct from a failed run. "Cannot" and "failed" are different
// answers to a consumer: the first means never offer the button, the second
// means show an error after it was clicked.
var ErrUpdateNotWritable = errors.New("claudecli: update target directory is not writable")

// UpdateNotWritableError reports that the updater's target directory is not
// writable by this process, so the update was never attempted.
type UpdateNotWritableError struct {
	// Method is the detected install method.
	Method InstallMethod

	// Dir is the directory the CLI's updater writes into for that method.
	Dir string

	// Err is the underlying filesystem error from the write probe.
	Err error
}

func (e *UpdateNotWritableError) Error() string {
	return fmt.Sprintf("claudecli: cannot update %s install: %s is not writable: %v", e.Method, e.Dir, e.Err)
}

func (e *UpdateNotWritableError) Is(target error) bool { return target == ErrUpdateNotWritable }

func (e *UpdateNotWritableError) Unwrap() error { return e.Err }

// ErrUpdateFailed matches the error returned by [Update] when the updater ran
// and exited non-zero.
var ErrUpdateFailed = errors.New("claudecli: claude update failed")

// UpdateFailedError reports that the updater ran and failed. The [UpdateResult]
// is still returned alongside it, so the before/after versions and the output
// are available for diagnosis.
type UpdateFailedError struct {
	// Path is the binary that was executed.
	Path string

	// ExitCode is the updater's exit status, or -1 when it never ran to
	// completion (killed by a signal, or the process could not start).
	ExitCode int

	// Output is the tail of the updater's combined stdout and stderr.
	Output string

	// Err is the underlying exec error.
	Err error
}

func (e *UpdateFailedError) Error() string {
	msg := fmt.Sprintf("claudecli: %s update exited %d", e.Path, e.ExitCode)
	if tail := lastLine(e.Output); tail != "" {
		msg += ": " + tail
	}
	return msg
}

func (e *UpdateFailedError) Is(target error) bool { return target == ErrUpdateFailed }

func (e *UpdateFailedError) Unwrap() error { return e.Err }

// UpdateResult describes one update run.
//
// # The exit code is not the answer
//
// VersionBefore and VersionAfter are read by running the CLI's own version
// probe either side of the update, and Changed compares them. That re-read is
// the only trustworthy signal that anything happened. A sibling SDK measured
// its CLI's updater exiting 0 and printing "Update ran successfully" while the
// command it shells out to was not installed at all — the updater reported the
// success of *launching* an update, not of applying one. Assume this CLI can do
// the same, and believe the version, not the status.
type UpdateResult struct {
	// Method is the install method that was updated. Always one the CLI
	// manages itself.
	Method InstallMethod

	// Path is the binary that was executed — the PATH entry recorded by
	// detection, never a fresh lookup. See [Update] on why that layer.
	Path string

	// VersionBefore is what the CLI reported for itself before the run, or ""
	// when that probe failed.
	VersionBefore string

	// VersionAfter is what it reports afterwards, or "" when the re-read
	// failed. An empty VersionAfter always comes with a non-nil error: the
	// update may well have succeeded, but nothing here can say so.
	VersionAfter string

	// Changed is true only when both versions are known and differ. A false
	// Changed with a nil error is the ordinary "already up to date" outcome,
	// and is indistinguishable from an updater that silently did nothing —
	// which is exactly why nothing here reports success on the exit code.
	Changed bool

	// ExitCode is the updater's exit status, kept for diagnostics. Do not
	// derive success from it.
	ExitCode int

	// Output is the tail of the updater's combined stdout and stderr, the last
	// maxUpdateOutputLines lines, newline-joined.
	Output string

	// Duration is how long the updater ran.
	Duration time.Duration
}

// UpdateOption configures a single [Update] call.
type UpdateOption func(*updateOptions)

type updateOptions struct {
	output   io.Writer
	progress func(string)
	timeout  time.Duration
}

// WithUpdateOutput streams the updater's combined stdout and stderr to w as it
// arrives, so a consumer can show live output for a run that takes seconds.
// Writes happen on the goroutine draining the process; w must not block for
// long and must be safe for the duration of the call.
func WithUpdateOutput(w io.Writer) UpdateOption {
	return func(o *updateOptions) { o.output = w }
}

// WithUpdateProgress calls fn once per output line as the updater emits it,
// mirroring [WithStderrCallback] for sessions. Lines are split on both "\n" and
// "\r" so a progress indicator that redraws in place still narrates.
func WithUpdateProgress(fn func(string)) UpdateOption {
	return func(o *updateOptions) { o.progress = fn }
}

// WithUpdateTimeout bounds the updater run. It applies only when the caller's
// context has no deadline of its own; a context deadline always wins.
func WithUpdateTimeout(d time.Duration) UpdateOption {
	return func(o *updateOptions) { o.timeout = d }
}

// Update runs the Claude CLI's own updater for the install on PATH, using the
// default client's binary.
//
// # Only installs the CLI manages itself
//
// Detection runs first and decides. [InstallNative] and [InstallNPMLocal] are
// the two layouts the CLI updates itself — the same two [DetectInstall] reports
// `claude update` for. Every other install belongs to something else: npm, a
// version manager, Homebrew, winget. Running the CLI's updater against those
// either fails or, worse, writes a second copy that shadows the first on PATH.
// So they are refused, with a [ManualUpdateError] carrying the command for the
// user to run. That refusal is a normal outcome, not a failure: it is the
// answer for most installs in the wild.
//
// # Which binary is executed
//
// The PATH entry recorded by detection ([InstallInfo.Path]), as an absolute
// path — never the bare word "claude", and never the symlink-resolved
// [InstallInfo.RealPath].
//
// Not the bare word, because a fresh lookup at exec time could reach a
// different copy than the one just detected. Two installs on one machine is not
// hypothetical: a native install in ~/.local/bin and an npm-global one in
// /usr/local/bin coexist happily, and whichever PATH reaches first is the one
// that answers.
//
// Not the resolved path either, and this is the subtler half. An npm-local
// install's PATH entry is a /bin/sh wrapper that execs
// <root>/node_modules/.bin/claude; resolving past it hands execution to a node
// script and drops the wrapper. A native install's PATH entry is a symlink into
// <data>/claude/versions/<version>; resolving past it runs the very binary the
// update is about to supersede. The PATH entry is the layer the user's own
// shell runs, so an update launched through it differs from a hand-typed
// `claude update` in nothing but the absolute path.
//
// # Preflight, then verify
//
// The directory the updater writes into is probed for writability first, and a
// failure there returns [ErrUpdateNotWritable] without running anything — a
// consumer renders "cannot update" differently from "update failed". Note that
// this is not the directory holding the binary on PATH: a native install writes
// to <data>/claude/versions while its PATH entry is a symlink in ~/.local/bin,
// and an npm-global install's package root is commonly root-owned while a
// native install's versions directory is not.
//
// Afterwards the version is re-read, because the exit code cannot be trusted;
// see [UpdateResult]. On failure the result is returned alongside the error.
// The re-read is also the only record this call leaves: the CLI writes its
// last-update-result file for background updates only, so an update driven from
// here is invisible to a later [DetectInstall] except in the version itself.
//
// The caller's context deadline is honoured. Without one the run is bounded by
// [WithUpdateTimeout], defaulting to ten minutes. A cancelled run is
// interrupted rather than killed outright so the updater can unwind its staged
// download instead of leaving a partial file behind.
func Update(ctx context.Context, opts ...UpdateOption) (*UpdateResult, error) {
	return defaultClient.Update(ctx, opts...)
}

// Update runs the Claude CLI's own updater for this client's binary. See the
// package-level [Update] for the full contract.
func (c *Client) Update(ctx context.Context, opts ...UpdateOption) (*UpdateResult, error) {
	result, err := runUpdate(ctx, c.binaryPath(), osUpdateEnv(), opts)
	if err != nil {
		c.log().Debug("update", "err", err)
		return result, err
	}
	c.log().Debug("update",
		"method", result.Method, "path", result.Path,
		"versionBefore", result.VersionBefore, "versionAfter", result.VersionAfter,
		"changed", result.Changed, "exitCode", result.ExitCode,
		"duration", result.Duration)
	return result, nil
}

// updateEnv is the set of ambient operations Update performs beyond detection,
// injectable so tests can drive every accept/refuse decision without a CLI on
// the machine.
type updateEnv struct {
	installEnv
	writable  func(dir string) error
	runUpdate func(ctx context.Context, binary string, onLine func(string)) (int, error)
}

func osUpdateEnv() updateEnv {
	return updateEnv{
		installEnv: osInstallEnv(),
		writable:   checkWritable,
		runUpdate:  execUpdate,
	}
}

// selfManaged reports whether the CLI's own updater owns this install. These
// are exactly the two methods updateCommand answers "claude update" for.
func selfManaged(m InstallMethod) bool {
	switch m {
	case InstallNative, InstallNPMLocal:
		return true
	default:
		return false
	}
}

func runUpdate(ctx context.Context, binary string, env updateEnv, opts []UpdateOption) (*UpdateResult, error) {
	var o updateOptions
	for _, opt := range opts {
		opt(&o)
	}

	info, err := detectInstall(ctx, binary, env.installEnv)
	if err != nil {
		return nil, err
	}
	if !selfManaged(info.Method) {
		return nil, &ManualUpdateError{Method: info.Method, Command: info.UpdateCmd}
	}

	target := updateTarget(info, env.installEnv)
	if err := env.writable(target); err != nil {
		return nil, &UpdateNotWritableError{Method: info.Method, Dir: target, Err: err}
	}

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		timeout := o.timeout
		if timeout <= 0 {
			timeout = defaultUpdateTimeout
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	ring := newStderrRing(maxUpdateOutputLines)
	onLine := func(line string) {
		ring.add(line)
		if o.progress != nil {
			o.progress(line)
		}
		if o.output != nil {
			_, _ = io.WriteString(o.output, line+"\n")
		}
	}

	start := time.Now()
	exitCode, runErr := env.runUpdate(ctx, info.Path, onLine)
	elapsed := time.Since(start)

	after, probeErr := reprobeVersion(ctx, info.Path, env.installEnv)

	result := &UpdateResult{
		Method:        info.Method,
		Path:          info.Path,
		VersionBefore: info.Version,
		VersionAfter:  after,
		Changed:       info.Version != "" && after != "" && info.Version != after,
		ExitCode:      exitCode,
		Output:        strings.Join(ring.lines(), "\n"),
		Duration:      elapsed,
	}

	if runErr != nil {
		failed := &UpdateFailedError{
			Path:     info.Path,
			ExitCode: exitCode,
			Output:   result.Output,
			Err:      runErr,
		}
		// A failed run whose version could not be re-read leaves two facts
		// worth reporting, not one.
		return result, errors.Join(failed, probeErr)
	}
	if probeErr != nil {
		return result, fmt.Errorf("claudecli: update ran but the installed version could not be re-read, so nothing confirms it applied: %w", probeErr)
	}
	return result, nil
}

// reprobeVersion re-reads the installed version after an update.
//
// It runs on a short bound detached from the caller's context. The update has
// already happened by this point, and the re-read is the only signal that says
// whether it did anything — inheriting a context the update itself may have
// just exhausted would throw that signal away exactly when it matters most.
func reprobeVersion(ctx context.Context, binary string, env installEnv) (string, error) {
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultInstallTimeout)
	defer cancel()
	return env.runVersion(probeCtx, binary)
}

// updateTarget reports the directory the CLI's updater writes into for this
// install — the one whose permissions decide whether an update can work at all.
//
// It is not the directory holding the binary on PATH. A native install's PATH
// entry is a symlink in ~/.local/bin, but the download lands in the versions
// directory the symlink points into. An npm-local install's entry is a wrapper
// script whose package tree sits a level up.
func updateTarget(info *InstallInfo, env installEnv) string {
	switch info.Method {
	case InstallNative:
		return nativeVersionsDir(info.RealPath, env.dataDir)
	case InstallNPMLocal:
		return npmLocalRoot(info.RealPath, env.configDir)
	default:
		return ""
	}
}

// nativeVersionsDir reports where the native installer keeps its versioned
// binaries. The resolved binary's own directory is preferred — it describes the
// install that actually runs — and the CLI's own <data>/claude/versions layout
// is the fallback when the binary sits elsewhere.
func nativeVersionsDir(realPath, dataDir string) string {
	if dir := filepath.Dir(realPath); isNativeVersionsLayout(strings.Split(normalizeInstallPath(dir), "/")) {
		return dir
	}
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "claude", "versions")
}

// npmLocalRoot reports the CLI-managed local install root, the directory
// holding the package.json and node_modules tree the updater reinstalls into.
func npmLocalRoot(realPath, configDir string) string {
	p := normalizeInstallPath(realPath)
	const marker = "/.claude/local/"
	if i := strings.Index(p, marker); i >= 0 {
		return filepath.FromSlash(p[:i+len(marker)-1])
	}
	// A relocated config directory ($CLAUDE_CONFIG_DIR) puts the same tree
	// somewhere the literal marker cannot match.
	if configDir != "" {
		return filepath.FromSlash(strings.TrimSuffix(normalizeInstallPath(configDir), "/") + "/local")
	}
	return ""
}

// checkWritable reports whether this process could write into dir.
//
// It creates and removes a temporary file rather than inspecting permission
// bits, because the bits are not the whole answer: ACLs, read-only mounts and
// root-owned package trees all deny a write that mode bits appear to allow. The
// probe file is a dotfile and is removed before returning, so no later
// DetectInstall can mistake it for an install artifact.
func checkWritable(dir string) error {
	if dir == "" {
		return errors.New("no update target directory could be determined")
	}
	f, err := os.CreateTemp(dir, ".claudecli-write-check-*")
	if err != nil {
		return err
	}
	name := f.Name()
	return errors.Join(f.Close(), os.Remove(name))
}

// execUpdate runs `<binary> update`, forwarding every output line to onLine as
// it arrives.
//
// Cancellation interrupts rather than kills: the updater stages its download
// and renames it into place, so giving it a moment to unwind is what keeps a
// cancelled run from leaving a partial file behind. The kill still happens
// after updateInterruptGrace, and on platforms where an interrupt cannot be
// delivered it happens immediately.
func execUpdate(ctx context.Context, binary string, onLine func(string)) (int, error) {
	cmd := exec.CommandContext(ctx, binary, "update")
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = updateInterruptGrace

	// os/exec serializes writes when Stdout and Stderr are the same comparable
	// writer, so one line splitter safely sees both streams interleaved.
	w := &lineWriter{fn: onLine}
	cmd.Stdout = w
	cmd.Stderr = w

	err := cmd.Run()
	w.flush()

	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), err
	}
	return -1, err
}

// lineWriter splits a byte stream into lines and hands each to fn. It breaks on
// carriage returns as well as newlines so an updater that redraws a progress
// indicator in place still produces something to narrate.
type lineWriter struct {
	fn  func(string)
	buf []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexAny(w.buf, "\n\r")
		if i < 0 {
			break
		}
		w.emit(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// flush emits whatever trailing text arrived without a line break.
func (w *lineWriter) flush() {
	if len(w.buf) > 0 {
		w.emit(string(w.buf))
		w.buf = nil
	}
}

func (w *lineWriter) emit(line string) {
	if line = strings.TrimRight(line, " \t"); line != "" && w.fn != nil {
		w.fn(line)
	}
}

// lastLine returns the final non-empty line of s, for one-line error messages.
func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}
