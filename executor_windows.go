//go:build windows

package claudecli

import (
	"context"
	"errors"
	"os/exec"
)

func setPlatformAttrs(cmd *exec.Cmd) {
	// On Windows, use the default behavior: cmd.Process.Kill() on context cancel.
	// No process group management available.
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
