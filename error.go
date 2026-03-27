package claudecli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Sentinel errors for CLI error classification.
// Use errors.Is to check from any error in the chain (Error, ErrorEvent, etc).
var (
	ErrRateLimit  = errors.New("rate limit")
	ErrAuth       = errors.New("authentication failed")
	ErrOverloaded = errors.New("API overloaded")
	ErrAPI        = errors.New("API error")
)

// RateLimitError carries retry timing for rate limit errors.
// Use errors.As to extract RetryAfter from any error in the chain.
type RateLimitError struct {
	RetryAfter time.Duration
	Message    string
}

func (e *RateLimitError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("rate limit: %s (retry after %s)", e.Message, e.RetryAfter)
	}
	return "rate limit: " + e.Message
}

func (e *RateLimitError) Is(target error) bool {
	return target == ErrRateLimit
}

// Error represents a CLI process failure with context.
type Error struct {
	ExitCode int
	Stderr   string
	Message  string
	class    error // sentinel or *RateLimitError; nil for unclassified
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("claudecli: exit %d: %s", e.ExitCode, e.Message)
	}
	if e.Stderr != "" {
		return fmt.Sprintf("claudecli: exit %d: %s", e.ExitCode, e.Stderr)
	}
	return fmt.Sprintf("claudecli: exit %d", e.ExitCode)
}

func (e *Error) Unwrap() []error {
	if e.class != nil {
		return []error{e.class}
	}
	return nil
}

// UnmarshalError is returned by RunJSON when the response text cannot be
// parsed as JSON. RawText contains the original model output for debugging.
type UnmarshalError struct {
	Err     error
	RawText string
}

func (e *UnmarshalError) Error() string {
	return fmt.Sprintf("unmarshal response: %s (raw text: %q)", e.Err, e.RawText)
}

func (e *UnmarshalError) Unwrap() error {
	return e.Err
}

// errorDetails is the internal representation of structured error JSON from CLI stderr.
type errorDetails struct {
	typ        string
	message    string
	retryAfter time.Duration
}

// parseErrorDetails tries to extract structured error JSON from stderr.
// Returns nil if no JSON object is found or parsing fails.
func parseErrorDetails(stderr string) *errorDetails {
	if d := tryParseErrorJSON(stderr); d != nil {
		return d
	}
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "{") {
			if d := tryParseErrorJSON(line); d != nil {
				return d
			}
		}
	}
	return nil
}

func tryParseErrorJSON(s string) *errorDetails {
	var raw struct {
		Type              string  `json:"type"`
		Message           string  `json:"message"`
		RetryAfterSeconds float64 `json:"retry_after_seconds"`
	}
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil
	}
	if raw.Type == "" {
		return nil
	}
	d := &errorDetails{
		typ:     raw.Type,
		message: raw.Message,
	}
	if raw.RetryAfterSeconds > 0 {
		d.retryAfter = time.Duration(raw.RetryAfterSeconds * float64(time.Second))
	}
	return d
}

// normalizeAPIErrorType maps Anthropic streaming API error type strings
// to the short codes used by classifyError.
func normalizeAPIErrorType(apiType string) string {
	switch apiType {
	case "overloaded_error":
		return "overloaded"
	case "rate_limit_error":
		return "rate_limit"
	case "authentication_error":
		return "auth"
	default:
		return apiType
	}
}

func classifyError(d *errorDetails) error {
	switch d.typ {
	case "rate_limit":
		return &RateLimitError{RetryAfter: d.retryAfter, Message: d.message}
	case "auth":
		return ErrAuth
	case "overloaded":
		return ErrOverloaded
	default:
		return nil
	}
}
