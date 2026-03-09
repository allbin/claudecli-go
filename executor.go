package claudecli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// Process represents a running CLI subprocess.
type Process struct {
	Stdout io.ReadCloser
	Stderr io.ReadCloser
	Wait   func() error
}

// StartConfig holds parameters for starting a CLI process.
type StartConfig struct {
	Args    []string
	Stdin   io.Reader
	Env     map[string]string
	WorkDir string
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

	cmd.Stdin = cfg.Stdin
	cmd.Env = buildEnv(cfg.Env)
	if runtime.GOOS != "windows" {
		cmd.Cancel = func() error {
			return cmd.Process.Signal(syscall.SIGTERM)
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
		Wait:   cmd.Wait,
	}, nil
}

// buildEnv merges the current environment with overrides, deduplicating keys.
func buildEnv(overrides map[string]string) []string {
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		if key == "CLAUDECODE" {
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
