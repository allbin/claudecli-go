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
	ErrInvalidRequest  = errors.New("invalid request")
	ErrAuth            = errors.New("authentication failed")
	ErrBilling         = errors.New("billing error")
	ErrPermission      = errors.New("permission denied")
	ErrNotFound        = errors.New("not found")
	ErrRequestTooLarge = errors.New("request too large")
	ErrRateLimit       = errors.New("rate limit")
	ErrAPI             = errors.New("API error")
	ErrOverloaded      = errors.New("API overloaded")
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
		s := e.Stderr
		if len(s) > 256 {
			s = s[:256] + "... (truncated, full stderr in Error.Stderr)"
		}
		return fmt.Sprintf("claudecli: exit %d: %s", e.ExitCode, s)
	}
	return fmt.Sprintf("claudecli: exit %d (no error details available — check CLI output or run with verbose logging)", e.ExitCode)
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
	case "invalid_request_error":
		return "invalid_request"
	case "authentication_error":
		return "auth"
	case "billing_error":
		return "billing"
	case "permission_error":
		return "permission"
	case "not_found_error":
		return "not_found"
	case "request_too_large":
		return "request_too_large"
	case "rate_limit_error":
		return "rate_limit"
	case "api_error":
		return "api"
	case "overloaded_error":
		return "overloaded"
	default:
		return apiType
	}
}

func classifyError(d *errorDetails) error {
	switch d.typ {
	case "invalid_request":
		return fmt.Errorf("%w: %s", ErrInvalidRequest, d.message)
	case "auth":
		return fmt.Errorf("%w: %s", ErrAuth, d.message)
	case "billing":
		return fmt.Errorf("%w: %s", ErrBilling, d.message)
	case "permission":
		return fmt.Errorf("%w: %s", ErrPermission, d.message)
	case "not_found":
		return fmt.Errorf("%w: %s", ErrNotFound, d.message)
	case "request_too_large":
		return fmt.Errorf("%w: %s", ErrRequestTooLarge, d.message)
	case "rate_limit":
		return &RateLimitError{RetryAfter: d.retryAfter, Message: d.message}
	case "api":
		return fmt.Errorf("%w: %s", ErrAPI, d.message)
	case "overloaded":
		return fmt.Errorf("%w: %s", ErrOverloaded, d.message)
	default:
		return nil
	}
}
