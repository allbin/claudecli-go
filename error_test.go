package claudecli

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParseErrorDetails_JSONOnly(t *testing.T) {
	stderr := `{"type":"rate_limit","message":"Rate limit exceeded","retry_after_seconds":30}`
	d := parseErrorDetails(stderr)
	if d == nil {
		t.Fatal("expected non-nil details")
	}
	if d.typ != "rate_limit" {
		t.Errorf("type = %q", d.typ)
	}
	if d.message != "Rate limit exceeded" {
		t.Errorf("message = %q", d.message)
	}
	if d.retryAfter != 30*time.Second {
		t.Errorf("retry_after = %v", d.retryAfter)
	}
}

func TestParseErrorDetails_MixedStderr(t *testing.T) {
	stderr := "some warning text\n{\"type\":\"auth\",\"message\":\"Invalid API key\"}\nmore text"
	d := parseErrorDetails(stderr)
	if d == nil {
		t.Fatal("expected non-nil details")
	}
	if d.typ != "auth" {
		t.Errorf("type = %q", d.typ)
	}
	if d.message != "Invalid API key" {
		t.Errorf("message = %q", d.message)
	}
}

func TestParseErrorDetails_PlainText(t *testing.T) {
	d := parseErrorDetails("something went wrong")
	if d != nil {
		t.Errorf("expected nil, got %+v", d)
	}
}

func TestParseErrorDetails_JSONWithoutType(t *testing.T) {
	d := parseErrorDetails(`{"message":"no type field"}`)
	if d != nil {
		t.Errorf("expected nil for JSON without type, got %+v", d)
	}
}

func TestParseErrorDetails_Empty(t *testing.T) {
	d := parseErrorDetails("")
	if d != nil {
		t.Errorf("expected nil for empty stderr, got %+v", d)
	}
}

func TestParseErrorDetails_NoRetryAfter(t *testing.T) {
	d := parseErrorDetails(`{"type":"overloaded","message":"Server busy"}`)
	if d == nil {
		t.Fatal("expected non-nil details")
	}
	if d.retryAfter != 0 {
		t.Errorf("expected zero retry_after, got %v", d.retryAfter)
	}
}

func TestErrorIs_RateLimit(t *testing.T) {
	e := &Error{ExitCode: 1, class: &RateLimitError{Message: "too fast"}}
	if !errors.Is(e, ErrRateLimit) {
		t.Error("expected errors.Is(e, ErrRateLimit)")
	}
	if errors.Is(e, ErrAuth) {
		t.Error("unexpected errors.Is(e, ErrAuth)")
	}
	if errors.Is(e, ErrOverloaded) {
		t.Error("unexpected errors.Is(e, ErrOverloaded)")
	}
}

func TestErrorIs_Auth(t *testing.T) {
	e := &Error{ExitCode: 1, class: ErrAuth}
	if !errors.Is(e, ErrAuth) {
		t.Error("expected errors.Is(e, ErrAuth)")
	}
	if errors.Is(e, ErrRateLimit) {
		t.Error("unexpected errors.Is(e, ErrRateLimit)")
	}
}

func TestErrorIs_Overloaded(t *testing.T) {
	e := &Error{ExitCode: 1, class: ErrOverloaded}
	if !errors.Is(e, ErrOverloaded) {
		t.Error("expected errors.Is(e, ErrOverloaded)")
	}
}

func TestErrorIs_NilClass(t *testing.T) {
	e := &Error{ExitCode: 1}
	if errors.Is(e, ErrRateLimit) || errors.Is(e, ErrAuth) || errors.Is(e, ErrOverloaded) {
		t.Error("expected no sentinel match with nil class")
	}
}

func TestErrorIs_AllSentinels(t *testing.T) {
	tests := []struct {
		name  string
		class error
		want  error
	}{
		{"invalid_request", ErrInvalidRequest, ErrInvalidRequest},
		{"billing", ErrBilling, ErrBilling},
		{"permission", ErrPermission, ErrPermission},
		{"not_found", ErrNotFound, ErrNotFound},
		{"request_too_large", ErrRequestTooLarge, ErrRequestTooLarge},
		{"api", ErrAPI, ErrAPI},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &Error{ExitCode: 1, class: tt.class}
			if !errors.Is(e, tt.want) {
				t.Errorf("expected errors.Is to match %v", tt.want)
			}
			if errors.Is(e, ErrRateLimit) {
				t.Error("unexpected match with ErrRateLimit")
			}
		})
	}
}

func TestErrorAs_RateLimitError(t *testing.T) {
	e := &Error{ExitCode: 1, class: &RateLimitError{
		RetryAfter: 30 * time.Second,
		Message:    "Rate limit exceeded",
	}}
	var rlErr *RateLimitError
	if !errors.As(e, &rlErr) {
		t.Fatal("expected errors.As to match *RateLimitError")
	}
	if rlErr.RetryAfter != 30*time.Second {
		t.Errorf("RetryAfter = %v", rlErr.RetryAfter)
	}
	if rlErr.Message != "Rate limit exceeded" {
		t.Errorf("Message = %q", rlErr.Message)
	}
}

func TestRateLimitError_Error(t *testing.T) {
	e := &RateLimitError{RetryAfter: 5 * time.Second, Message: "slow down"}
	got := e.Error()
	if got != "rate limit: slow down (retry after 5s)" {
		t.Errorf("got %q", got)
	}

	e2 := &RateLimitError{Message: "slow down"}
	got2 := e2.Error()
	if got2 != "rate limit: slow down" {
		t.Errorf("got %q", got2)
	}
}

func TestError_ErrorTruncatesLongStderr(t *testing.T) {
	long := strings.Repeat("x", 500)
	e := &Error{ExitCode: 1, Stderr: long}
	got := e.Error()
	if len(got) > 350 {
		t.Errorf("Error() not truncated: len=%d", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Error("missing truncation marker")
	}
}

func TestError_ErrorShortStderrUnchanged(t *testing.T) {
	e := &Error{ExitCode: 1, Stderr: "short error"}
	got := e.Error()
	if !strings.Contains(got, "short error") {
		t.Errorf("Error() = %q", got)
	}
	if strings.Contains(got, "truncated") {
		t.Error("should not truncate short stderr")
	}
}

func TestError_EmptyMessageAndStderr(t *testing.T) {
	e := &Error{ExitCode: 1}
	got := e.Error()
	if !strings.Contains(got, "no error details available") {
		t.Errorf("expected diagnostic hint, got %q", got)
	}
	if !strings.Contains(got, "exit 1") {
		t.Errorf("expected exit code, got %q", got)
	}
}

func TestError_MessageTakesPrecedence(t *testing.T) {
	e := &Error{ExitCode: 1, Message: "auth failed", Stderr: "raw stderr"}
	got := e.Error()
	if !strings.Contains(got, "auth failed") {
		t.Errorf("expected Message in output, got %q", got)
	}
	if strings.Contains(got, "raw stderr") {
		t.Errorf("Stderr should not appear when Message is set, got %q", got)
	}
}

func TestProcessExitError_EmptyStderr(t *testing.T) {
	// Non-ExitError with empty stderr: message falls back to err.Error().
	cliErr := processExitError(fmt.Errorf("exit status 1"), "")
	if cliErr.Message != "exit status 1" {
		t.Errorf("expected generic error message, got %q", cliErr.Message)
	}
	if cliErr.ExitCode != -1 {
		t.Errorf("expected exit code -1 for non-ExitError, got %d", cliErr.ExitCode)
	}
}

func TestProcessExitError_StderrAllJSON(t *testing.T) {
	// Stderr with only JSON lines that lack a "type" field — inferErrorMessage
	// should return empty, processExitError falls back to err.Error().
	stderr := "{\"foo\":\"bar\"}\n{\"baz\":123}"
	cliErr := processExitError(fmt.Errorf("exit status 1"), stderr)
	// No structured error details, no non-JSON lines, so message comes from err.Error()
	if cliErr.Message != "exit status 1" {
		t.Errorf("expected fallback to err.Error(), got %q", cliErr.Message)
	}
}

func TestInferErrorMessage_NodeStackTrace(t *testing.T) {
	stderr := "node:internal/modules/cjs/loader:1228\n  throw err;\n  ^\nError: Cannot find module '/usr/lib/node_modules/claude/bin'\n    at Module._resolveFilename (node:internal/modules/cjs/loader:1225:15)"
	got := inferErrorMessage(stderr)
	// Should pick up the last non-empty non-JSON line
	if got == "" {
		t.Error("expected non-empty message from Node stack trace")
	}
	// Should be the last meaningful line (the stack frame)
	if !strings.Contains(got, "Module._resolveFilename") {
		t.Errorf("expected last line of stack trace, got %q", got)
	}
}

func TestInferErrorMessage_EmptyStderr(t *testing.T) {
	got := inferErrorMessage("")
	if got != "" {
		t.Errorf("expected empty for empty stderr, got %q", got)
	}
}

func TestNormalizeAPIErrorType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"invalid_request_error", "invalid_request"},
		{"authentication_error", "auth"},
		{"billing_error", "billing"},
		{"permission_error", "permission"},
		{"not_found_error", "not_found"},
		{"request_too_large", "request_too_large"},
		{"rate_limit_error", "rate_limit"},
		{"api_error", "api"},
		{"overloaded_error", "overloaded"},
		{"", ""},
		{"some_future_type", "some_future_type"},
	}
	for _, tt := range tests {
		got := normalizeAPIErrorType(tt.input)
		if got != tt.want {
			t.Errorf("normalizeAPIErrorType(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		typ     string
		wantNil bool
		target  error
	}{
		{"invalid_request", false, ErrInvalidRequest},
		{"auth", false, ErrAuth},
		{"billing", false, ErrBilling},
		{"permission", false, ErrPermission},
		{"not_found", false, ErrNotFound},
		{"request_too_large", false, ErrRequestTooLarge},
		{"rate_limit", false, ErrRateLimit},
		{"api", false, ErrAPI},
		{"overloaded", false, ErrOverloaded},
		{"unknown_type", true, nil},
	}
	for _, tt := range tests {
		d := &errorDetails{typ: tt.typ, message: "msg"}
		got := classifyError(d)
		if tt.wantNil {
			if got != nil {
				t.Errorf("classifyError(%q) = %v, want nil", tt.typ, got)
			}
			continue
		}
		if got == nil {
			t.Errorf("classifyError(%q) = nil, want non-nil", tt.typ)
			continue
		}
		if !errors.Is(got, tt.target) {
			t.Errorf("classifyError(%q): errors.Is failed for %v", tt.typ, tt.target)
		}
	}
}
