//go:build windows

package claudecli

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// createNoWindow is the Windows CREATE_NO_WINDOW process creation flag. When
// the parent has no console (a detached server, service, or GUI/tray app),
// Windows would otherwise allocate a fresh console window for the child, which
// flashes on screen. Setting this flag suppresses that console.
const createNoWindow = 0x08000000

// platformProc holds the job object confining one CLI spawn. The CLI starts
// MCP servers (node/npx) which may spawn their own children (Chromium);
// killing only claude.exe leaves that tree running. A kill-on-close job
// object ties the whole tree to this handle: TerminateJobObject on context
// cancel kills every member, and closing the handle after Wait reaps any
// stragglers that outlived a clean CLI exit.
type platformProc struct {
	mu       sync.Mutex
	job      windows.Handle // 0 when job setup failed (degraded to single-PID kill)
	assigned bool           // child successfully placed in the job
}

// hideConsole marks cmd to run without a console window. Every direct CLI
// spawn needs this, not just the executor's: when the parent is windowless,
// each bare spawn — an auth-status poll, a `-v` probe — flashes a console
// on screen.
func hideConsole(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// OR the flag in so we don't clobber any other creation flags.
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}

func setPlatformAttrs(cmd *exec.Cmd) *platformProc {
	hideConsole(cmd)
	p := &platformProc{job: newKillOnCloseJob()}
	// Cancel must be installed before Start (os/exec reads it from the
	// context-watch goroutine). Kill the whole job; if the job isn't usable,
	// fall back to killing just the CLI process — today's behavior.
	cmd.Cancel = func() error { return p.killTree(cmd) }
	cmd.WaitDelay = 5 * time.Second
	return p
}

// setUpdateCancel configures cancellation for the `claude update` spawn.
// Windows cannot deliver a console interrupt from a windowless parent
// (GenerateConsoleCtrlEvent only reaches processes on the caller's own
// console), so cancellation is an immediate tree kill via the job object:
// no grace period for the updater to unwind its staged download, but no
// orphaned npm/node children either.
func setUpdateCancel(cmd *exec.Cmd) *platformProc {
	hideConsole(cmd)
	p := &platformProc{job: newKillOnCloseJob()}
	cmd.Cancel = func() error { return p.killTree(cmd) }
	return p
}

// killTree terminates cmd's whole process tree via the job object,
// degrading to killing just the direct child when the job is unusable.
// The Terminate call happens under the mutex: killTree can be invoked from
// a goroutine release() does not synchronize with, and a handle used after
// CloseHandle could have been recycled to name someone else's object.
func (p *platformProc) killTree(cmd *exec.Cmd) error {
	p.mu.Lock()
	err := errors.New("no job object")
	if p.job != 0 && p.assigned {
		err = windows.TerminateJobObject(p.job, 1)
	}
	p.mu.Unlock()
	if err == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// newKillOnCloseJob creates an anonymous job object whose members are all
// terminated when its last handle closes. Returns 0 on failure; the caller
// then degrades to single-PID kill rather than failing the spawn.
func newKillOnCloseJob() windows.Handle {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return 0
	}
	return job
}

// afterStart places the just-started child in the job. os/exec offers no
// CREATE_SUSPENDED path, so a child that forked before this call could
// escape the job; in practice the CLI takes far longer than that to start
// its MCP servers. On any failure the job is dropped and cancellation
// degrades to killing only the CLI process.
func (p *platformProc) afterStart(cmd *exec.Cmd) {
	if p.job == 0 || cmd.Process == nil {
		return
	}
	// AssignProcessToJobObject needs PROCESS_SET_QUOTA|PROCESS_TERMINATE on
	// the process handle. os.Process's own handle is unexported, so open a
	// second one by pid; the pid can't be recycled here because os.Process
	// still holds its handle.
	proc, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		p.release()
		return
	}
	p.mu.Lock()
	if p.job != 0 {
		if err := windows.AssignProcessToJobObject(p.job, proc); err == nil {
			p.assigned = true
		}
	}
	assigned := p.assigned
	p.mu.Unlock()
	windows.CloseHandle(proc)
	if !assigned {
		p.release()
	}
}

// release closes the job handle. Called after Wait returns (kill-on-close
// then terminates anything still in the job) or when job setup fails.
// Idempotent. CloseHandle happens under the mutex so a concurrent killTree
// either terminates the still-open job or sees it already gone — never a
// recycled handle value.
func (p *platformProc) release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job != 0 {
		windows.CloseHandle(p.job)
		p.job = 0
	}
	p.assigned = false
}

// buildPlatformCmd creates the exec.Cmd. When the resolved binary is npm's
// cmd.exe shim, run the wrapped entry script with node directly (see
// shim.go): os/exec refuses .bat/.cmd args it cannot safely escape, and the
// bypass also drops the cmd.exe layer from the process tree. Falls back to
// running the shim when node is missing or the layout is unconfirmed.
func buildPlatformCmd(ctx context.Context, binary string, args []string) *exec.Cmd {
	if cliJS, ok := findShimCLIJS(binary); ok {
		if node, err := exec.LookPath("node"); err == nil {
			return exec.CommandContext(ctx, node, append([]string{cliJS}, args...)...)
		}
	}
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
