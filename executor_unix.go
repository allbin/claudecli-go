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

func setPlatformAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second
}

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
