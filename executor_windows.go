//go:build windows

package claudecli

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
)

// createNoWindow is the Windows CREATE_NO_WINDOW process creation flag. When
// the parent has no console (a detached server, service, or GUI/tray app),
// Windows would otherwise allocate a fresh console window for the child, which
// flashes on screen. Setting this flag suppresses that console.
const createNoWindow = 0x08000000

func setPlatformAttrs(cmd *exec.Cmd) {
	// On Windows, use the default behavior: cmd.Process.Kill() on context cancel.
	// No process group management available.
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// OR the flag in so we don't clobber any other creation flags.
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}

// buildPlatformCmd creates the exec.Cmd. No special wrapping needed on Windows.
func buildPlatformCmd(ctx context.Context, binary string, args []string) *exec.Cmd {
	return exec.CommandContext(ctx, binary, args...)
}

// extractExitDetails returns the exit code from a Wait() error. Windows
// has no signals, so the signal field is always empty.
func extractExitDetails(err error) (signal string, exitCode int) {
	if err == nil {
		return "", 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return "", -1
	}
	return "", exitErr.ExitCode()
}
