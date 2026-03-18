package claudecli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// SDKVersion is the version of this Go SDK, sent as an env var to the CLI.
const SDKVersion = "0.2.0"

// Process represents a running CLI subprocess.
type Process struct {
	Stdout io.ReadCloser
	Stderr io.ReadCloser
	Stdin  io.WriteCloser // nil when stdin was closed after initial write
	Wait   func() error
}

// StartConfig holds parameters for starting a CLI process.
type StartConfig struct {
	Args                    []string
	Stdin                   io.Reader
	Env                     map[string]string
	WorkDir                 string
	KeepStdinOpen           bool // if true, don't close stdin after initial write
	EnableFileCheckpointing bool
	SkipVersionCheck        bool
}

// Executor controls how the Claude CLI process is spawned.
// Implement this interface to customize execution (e.g. Docker, SSH).
type Executor interface {
	Start(ctx context.Context, cfg *StartConfig) (*Process, error)
}

// LocalExecutor spawns the Claude CLI as a local subprocess.
type LocalExecutor struct {
	// BinaryPath overrides the CLI binary. Defaults to "claude".
	BinaryPath string

	versionOnce sync.Once
	versionErr  error
}

// NewLocalExecutor returns an executor that runs Claude CLI locally.
func NewLocalExecutor() *LocalExecutor {
	return &LocalExecutor{}
}

func (e *LocalExecutor) Start(ctx context.Context, cfg *StartConfig) (*Process, error) {
	binary := e.BinaryPath
	if binary == "" {
		binary = "claude"
	}

	if !cfg.SkipVersionCheck {
		e.versionOnce.Do(func() {
			vctx, vcancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer vcancel()
			err := CheckCLIVersion(vctx, binary)
			if _, ok := err.(*VersionError); ok {
				e.versionErr = err
			}
		})
		if e.versionErr != nil {
			return nil, e.versionErr
		}
	}

	resolvedBinary, err := exec.LookPath(binary)
	if err != nil {
		return nil, fmt.Errorf("claude binary not found: %w", err)
	}

	cmd := exec.CommandContext(ctx, resolvedBinary, cfg.Args...)
	if runtime.GOOS == "linux" {
		if stdbuf, err := exec.LookPath("stdbuf"); err == nil {
			cmd = exec.CommandContext(ctx, stdbuf, append([]string{"-oL", resolvedBinary}, cfg.Args...)...)
		}
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	if cfg.Stdin != nil {
		pipeClose := sync.OnceFunc(func() { stdinPipe.Close() })

		// Close pipe on context cancel to unblock the copy goroutine.
		copyDone := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				pipeClose()
			case <-copyDone:
			}
		}()

		go func() {
			defer close(copyDone)
			io.Copy(stdinPipe, cfg.Stdin)
			if !cfg.KeepStdinOpen {
				pipeClose()
			}
		}()
	} else if !cfg.KeepStdinOpen {
		stdinPipe.Close()
	}

	envOverrides := cfg.Env
	if cfg.EnableFileCheckpointing {
		envOverrides = make(map[string]string, len(cfg.Env)+1)
		maps.Copy(envOverrides, cfg.Env)
		envOverrides["CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING"] = "1"
	}
	cmd.Env = buildEnv(envOverrides)
	if runtime.GOOS != "windows" {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			// Signal the process group (negative PID) to kill children too.
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		}
		cmd.WaitDelay = 5 * time.Second
	}
	if cfg.WorkDir != "" {
		cmd.Dir = cfg.WorkDir
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	return &Process{
		Stdout: stdout,
		Stderr: stderr,
		Stdin:  stdinPipe,
		Wait:   cmd.Wait,
	}, nil
}

// buildEnv merges the current environment with overrides, deduplicating keys.
func buildEnv(overrides map[string]string) []string {
	env := make([]string, 0, len(os.Environ())+len(overrides)+2)
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		if key == "CLAUDECODE" || key == "CLAUDE_CODE_ENTRYPOINT" {
			continue
		}
		if _, ok := overrides[key]; ok {
			continue
		}
		env = append(env, e)
	}
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	env = append(env, "CLAUDE_CODE_ENTRYPOINT=sdk-go")
	env = append(env, "CLAUDE_AGENT_SDK_VERSION="+SDKVersion)
	return env
}

// FixtureExecutor replays JSONL from an io.Reader for testing.
type FixtureExecutor struct {
	reader io.Reader
}

// NewFixtureExecutor creates an executor that replays JSONL fixtures.
func NewFixtureExecutor(r io.Reader) *FixtureExecutor {
	return &FixtureExecutor{reader: r}
}

// NewFixtureExecutorFromFile creates an executor that replays a JSONL file.
func NewFixtureExecutorFromFile(path string) (*FixtureExecutor, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return &FixtureExecutor{reader: bytes.NewReader(data)}, nil
}

func (e *FixtureExecutor) Start(_ context.Context, _ *StartConfig) (*Process, error) {
	pr, pw := io.Pipe()

	go func() {
		_, _ = io.Copy(pw, e.reader)
		pw.Close()
	}()

	return &Process{
		Stdout: pr,
		Stderr: io.NopCloser(strings.NewReader("")),
		Wait:   func() error { return nil },
	}, nil
}

// BidiFixtureExecutor supports bidirectional I/O for testing sessions.
type BidiFixtureExecutor struct {
	StdoutWriter io.WriteCloser
	StdinReader  io.ReadCloser
	stdoutReader io.ReadCloser
	stdinWriter  io.WriteCloser
}

// NewBidiFixtureExecutor creates a bidirectional executor for session tests.
func NewBidiFixtureExecutor() *BidiFixtureExecutor {
	stdoutR, stdoutW := io.Pipe()
	stdinR, stdinW := io.Pipe()
	return &BidiFixtureExecutor{
		StdoutWriter: stdoutW,
		StdinReader:  stdinR,
		stdoutReader: stdoutR,
		stdinWriter:  stdinW,
	}
}

func (e *BidiFixtureExecutor) Start(_ context.Context, _ *StartConfig) (*Process, error) {
	return &Process{
		Stdout: e.stdoutReader,
		Stderr: io.NopCloser(strings.NewReader("")),
		Stdin:  e.stdinWriter,
		Wait:   func() error { return nil },
	}, nil
}
