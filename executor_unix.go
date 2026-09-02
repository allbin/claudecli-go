//go:build !windows

package claudecli

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

// platformProc holds per-spawn platform resources. On unix the process group
// created by Setpgid needs no handle, so there is nothing to hold or release.
type platformProc struct{}

// hideConsole suppresses the child's console window on Windows. No-op on unix.
func hideConsole(cmd *exec.Cmd) {}

func setPlatformAttrs(cmd *exec.Cmd) *platformProc {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second
	return &platformProc{}
}

// setUpdateCancel configures cancellation for the `claude update` spawn:
// SIGINT the whole process group so the updater — and any npm/node children
// doing the actual work — can unwind a staged download. The caller's
// WaitDelay provides the eventual kill.
func setUpdateCancel(cmd *exec.Cmd) *platformProc {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
	}
	return &platformProc{}
}

// killTree forcibly terminates cmd's whole process tree (SIGKILL to the
// group), falling back to killing the direct child if the group is gone.
func (p *platformProc) killTree(cmd *exec.Cmd) error {
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// afterStart finalizes process-tree confinement once the child is running.
// Nothing to do on unix: Setpgid took effect at spawn.
func (p *platformProc) afterStart(cmd *exec.Cmd) {}

// release frees per-spawn platform resources. Nothing held on unix.
func (p *platformProc) release() {}

// buildPlatformCmd creates the exec.Cmd with platform-specific handling.
// On Linux, wraps with stdbuf -oL to force line-buffered stdout when available.
func buildPlatformCmd(ctx context.Context, binary string, args []string) *exec.Cmd {
	if runtime.GOOS == "linux" {
		if stdbuf, err := exec.LookPath("stdbuf"); err == nil {
			return exec.CommandContext(ctx, stdbuf, append([]string{"-oL", binary}, args...)...)
		}
	}
	return exec.CommandContext(ctx, binary, args...)
}

// extractExitDetails returns the signal name and exit code from a Wait()
// error. Returns ("", 0) on nil error (clean exit) and ("", -1) when the
// error is not an *exec.ExitError (e.g. start failure, ctx cancel before
// process started).
func extractExitDetails(err error) (signal string, exitCode int) {
	if err == nil {
		return "", 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return "", -1
	}
	code := exitErr.ExitCode()
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return signalName(ws.Signal()), code
	}
	return "", code
}

func signalName(s syscall.Signal) string {
	switch s {
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	case syscall.SIGABRT:
		return "SIGABRT"
	case syscall.SIGSEGV:
		return "SIGSEGV"
	case syscall.SIGBUS:
		return "SIGBUS"
	case syscall.SIGPIPE:
		return "SIGPIPE"
	}
	return s.String()
}
