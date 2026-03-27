package claudecli

import (
	"errors"
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

func TestNormalizeAPIErrorType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"overloaded_error", "overloaded"},
		{"rate_limit_error", "rate_limit"},
		{"authentication_error", "auth"},
		{"api_error", "api_error"},
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
		{"rate_limit", false, ErrRateLimit},
		{"auth", false, ErrAuth},
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
