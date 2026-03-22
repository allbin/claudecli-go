//go:build windows

package claudecli

import "os/exec"

func setPlatformAttrs(cmd *exec.Cmd) {
	// On Windows, use the default behavior: cmd.Process.Kill() on context cancel.
	// No process group management available.
}
