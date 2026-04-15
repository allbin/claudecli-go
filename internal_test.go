package claudecli

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestStripCodeFence_JSONFence(t *testing.T) {
	input := "```json\n{\"key\": \"value\"}\n```"
	got := stripCodeFence(input)
	want := `{"key": "value"}`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripCodeFence_NoLang(t *testing.T) {
	input := "```\nhello world\n```"
	got := stripCodeFence(input)
	want := "hello world"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripCodeFence_FourBackticks(t *testing.T) {
	input := "````\nshould not strip\n````"
	got := stripCodeFence(input)
	if got != input {
		t.Errorf("expected input unchanged, got %q", got)
	}
}

func TestStripCodeFence_NoFence(t *testing.T) {
	input := `{"plain": "json"}`
	got := stripCodeFence(input)
	if got != input {
		t.Errorf("expected input unchanged, got %q", got)
	}
}

func TestStripCodeFence_Unclosed(t *testing.T) {
	input := "```json\n{\"key\": \"value\"}"
	got := stripCodeFence(input)
	if got != input {
		t.Errorf("expected input unchanged, got %q", got)
	}
}

func TestStripCodeFence_Empty(t *testing.T) {
	got := stripCodeFence("")
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestProcessExitError_ExitCode(t *testing.T) {
	// Produce a real exec.ExitError with exit code 42.
	cmd := exec.Command("sh", "-c", "exit 42")
	exitErr := cmd.Run()

	stderr := `{"type":"overloaded","message":"API overloaded"}`
	result := processExitError(exitErr, stderr)

	if result.ExitCode != 42 {
		t.Errorf("exit code = %d, want 42", result.ExitCode)
	}
	if result.Stderr != stderr {
		t.Errorf("stderr = %q, want %q", result.Stderr, stderr)
	}
	if result.Message != "API overloaded" {
		t.Errorf("message = %q, want 'API overloaded'", result.Message)
	}
	if !errors.Is(result, ErrOverloaded) {
		t.Error("expected errors.Is(result, ErrOverloaded)")
	}
}

func TestInferErrorMessage(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   string
	}{
		{"command not found", "bash: claude: command not found", "claude binary not found (is it installed and in PATH?)"},
		{"no such file", "/usr/bin/claude: no such file or directory", "file or directory not found (check working directory and binary path)"},
		{"permission denied", "bash: /usr/bin/claude: Permission denied", "permission denied running claude binary"},
		{"enoent only", "spawn ENOENT", "file or directory not found (ENOENT)"},
		{"eacces only", "spawn EACCES", "permission denied (EACCES)"},
		{"enoent with message", "Error: ENOENT: no such file or directory", "file or directory not found (check working directory and binary path)"},
		{"eacces with message", "Error: EACCES: permission denied", "permission denied running claude binary"},
		{"plain text", "something went wrong", "something went wrong"},
		{"multi-line last wins", "warning: slow\nerror: crash here", "error: crash here"},
		{"skips JSON lines", "some warning\n" + `{"type":"auth","message":"bad"}`, "some warning"},
		{"empty", "", ""},
		{"truncates long line", strings.Repeat("x", 300), strings.Repeat("x", 200) + "..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferErrorMessage(tt.stderr)
			if got != tt.want {
				t.Errorf("inferErrorMessage(%q) = %q, want %q", tt.stderr, got, tt.want)
			}
		})
	}
}

func TestStderrRing(t *testing.T) {
	r := newStderrRing(3)
	r.add("a")
	r.add("b")
	got := r.lines()
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("partial fill: %v", got)
	}

	r.add("c")
	r.add("d") // wraps: ["d", "b", "c"], pos=1, full=true → lines: b, c, d
	got = r.lines()
	if len(got) != 3 || got[0] != "b" || got[1] != "c" || got[2] != "d" {
		t.Errorf("after wrap: %v", got)
	}

	r.add("e")
	r.add("f")
	got = r.lines()
	if len(got) != 3 || got[0] != "d" || got[1] != "e" || got[2] != "f" {
		t.Errorf("after double wrap: %v", got)
	}
}

func TestLineCaptureReader(t *testing.T) {
	ring := newStderrRing(5)
	input := "line1\nline2\nline3\n"
	r := &lineCaptureReader{r: strings.NewReader(input), ring: ring}

	// Read all bytes through the wrapper.
	buf := make([]byte, 1024)
	var total int
	for {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			break
		}
	}
	r.flush()

	got := ring.lines()
	if len(got) != 3 || got[0] != "line1" || got[1] != "line2" || got[2] != "line3" {
		t.Errorf("captured lines = %v, want [line1, line2, line3]", got)
	}
	if string(buf[:total]) != input {
		t.Errorf("passthrough data = %q, want %q", string(buf[:total]), input)
	}
}

func TestLineCaptureReader_PartialFlush(t *testing.T) {
	ring := newStderrRing(5)
	input := "line1\npartial" // no trailing newline
	r := &lineCaptureReader{r: strings.NewReader(input), ring: ring}

	buf := make([]byte, 1024)
	for {
		_, err := r.Read(buf)
		if err != nil {
			break
		}
	}

	// Before flush, only complete lines captured.
	got := ring.lines()
	if len(got) != 1 || got[0] != "line1" {
		t.Errorf("before flush: %v", got)
	}

	r.flush()
	got = ring.lines()
	if len(got) != 2 || got[1] != "partial" {
		t.Errorf("after flush: %v", got)
	}
}

func TestLineCaptureReader_SmallReads(t *testing.T) {
	// Force byte-at-a-time reads to ensure line detection works across
	// Read boundaries.
	ring := newStderrRing(5)
	input := "ab\ncd\n"
	r := &lineCaptureReader{r: strings.NewReader(input), ring: ring}

	buf := make([]byte, 1) // 1-byte reads
	for {
		_, err := r.Read(buf)
		if err != nil {
			break
		}
	}
	r.flush()

	got := ring.lines()
	if len(got) != 2 || got[0] != "ab" || got[1] != "cd" {
		t.Errorf("small reads: %v", got)
	}
}

func TestBuildEnv_EntrypointDefault(t *testing.T) {
	env := buildEnv(nil)
	var found bool
	for _, e := range env {
		if e == "CLAUDE_CODE_ENTRYPOINT=sdk-go" {
			found = true
		}
	}
	if !found {
		t.Error("CLAUDE_CODE_ENTRYPOINT=sdk-go not set as default")
	}
}

func TestBuildEnv_EntrypointUserOverride(t *testing.T) {
	env := buildEnv(map[string]string{"CLAUDE_CODE_ENTRYPOINT": "custom-caller"})
	var found string
	for _, e := range env {
		if len(e) > len("CLAUDE_CODE_ENTRYPOINT=") && e[:len("CLAUDE_CODE_ENTRYPOINT=")] == "CLAUDE_CODE_ENTRYPOINT=" {
			found = e
		}
	}
	if found != "CLAUDE_CODE_ENTRYPOINT=custom-caller" {
		t.Errorf("expected user override, got %q", found)
	}
	// Should not contain duplicate sdk-go entry
	var count int
	for _, e := range env {
		if len(e) > len("CLAUDE_CODE_ENTRYPOINT=") && e[:len("CLAUDE_CODE_ENTRYPOINT=")] == "CLAUDE_CODE_ENTRYPOINT=" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 CLAUDE_CODE_ENTRYPOINT entry, got %d", count)
	}
}

func TestBuildEnv_SDKVersionAlwaysSet(t *testing.T) {
	// SDK version should be set even when user provides CLAUDE_CODE_ENTRYPOINT
	env := buildEnv(map[string]string{"CLAUDE_CODE_ENTRYPOINT": "custom"})
	var found bool
	for _, e := range env {
		if len(e) > len("CLAUDE_AGENT_SDK_VERSION=") && e[:len("CLAUDE_AGENT_SDK_VERSION=")] == "CLAUDE_AGENT_SDK_VERSION=" {
			found = true
		}
	}
	if !found {
		t.Error("CLAUDE_AGENT_SDK_VERSION not set")
	}
}

func TestProcessExitError_SterrPassthrough(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 1")
	exitErr := cmd.Run()

	stderr := "plain error text"
	result := processExitError(exitErr, stderr)

	if result.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", result.ExitCode)
	}
	if result.Stderr != stderr {
		t.Errorf("stderr = %q, want %q", result.Stderr, stderr)
	}
}
