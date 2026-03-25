package claudecli

import (
	"errors"
	"os/exec"
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
