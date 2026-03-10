package claudecli

import (
	"testing"
	"time"
)

func TestParseErrorDetails_JSONOnly(t *testing.T) {
	stderr := `{"type":"rate_limit","message":"Rate limit exceeded","retry_after_seconds":30}`
	d := parseErrorDetails(stderr)
	if d == nil {
		t.Fatal("expected non-nil details")
	}
	if d.Type != "rate_limit" {
		t.Errorf("type = %q", d.Type)
	}
	if d.Message != "Rate limit exceeded" {
		t.Errorf("message = %q", d.Message)
	}
	if d.RetryAfter != 30*time.Second {
		t.Errorf("retry_after = %v", d.RetryAfter)
	}
}

func TestParseErrorDetails_MixedStderr(t *testing.T) {
	stderr := "some warning text\n{\"type\":\"auth\",\"message\":\"Invalid API key\"}\nmore text"
	d := parseErrorDetails(stderr)
	if d == nil {
		t.Fatal("expected non-nil details")
	}
	if d.Type != "auth" {
		t.Errorf("type = %q", d.Type)
	}
	if d.Message != "Invalid API key" {
		t.Errorf("message = %q", d.Message)
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
	if d.RetryAfter != 0 {
		t.Errorf("expected zero retry_after, got %v", d.RetryAfter)
	}
}

func TestErrorIsRateLimit(t *testing.T) {
	e := &Error{ExitCode: 1, Details: &ErrorDetails{Type: "rate_limit"}}
	if !e.IsRateLimit() {
		t.Error("expected IsRateLimit true")
	}
	if e.IsAuth() {
		t.Error("expected IsAuth false")
	}
	if e.IsOverloaded() {
		t.Error("expected IsOverloaded false")
	}
}

func TestErrorIsAuth(t *testing.T) {
	e := &Error{ExitCode: 1, Details: &ErrorDetails{Type: "auth"}}
	if !e.IsAuth() {
		t.Error("expected IsAuth true")
	}
	if e.IsRateLimit() {
		t.Error("expected IsRateLimit false")
	}
}

func TestErrorIsOverloaded(t *testing.T) {
	e := &Error{ExitCode: 1, Details: &ErrorDetails{Type: "overloaded"}}
	if !e.IsOverloaded() {
		t.Error("expected IsOverloaded true")
	}
}

func TestErrorHelpers_NilDetails(t *testing.T) {
	e := &Error{ExitCode: 1}
	if e.IsRateLimit() || e.IsAuth() || e.IsOverloaded() {
		t.Error("expected all false with nil details")
	}
}
