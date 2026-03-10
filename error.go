package claudecli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ErrorDetails contains structured error information parsed from CLI stderr.
type ErrorDetails struct {
	Type       string        `json:"type"`
	Message    string        `json:"message"`
	RetryAfter time.Duration // parsed from retry_after_seconds if present
}

// Error represents a CLI process failure with context.
type Error struct {
	ExitCode int
	Stderr   string
	Message  string
	Details  *ErrorDetails
}

// IsRateLimit returns true if this is a rate limit error.
func (e *Error) IsRateLimit() bool {
	return e.Details != nil && e.Details.Type == "rate_limit"
}

// IsAuth returns true if this is an authentication error.
func (e *Error) IsAuth() bool {
	return e.Details != nil && e.Details.Type == "auth"
}

// IsOverloaded returns true if the API is overloaded.
func (e *Error) IsOverloaded() bool {
	return e.Details != nil && e.Details.Type == "overloaded"
}

// parseErrorDetails tries to extract structured error JSON from stderr.
// Returns nil if no JSON object is found or parsing fails.
func parseErrorDetails(stderr string) *ErrorDetails {
	// Try the whole string first
	if d := tryParseErrorJSON(stderr); d != nil {
		return d
	}
	// Try each line — stderr may mix text and JSON
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

func tryParseErrorJSON(s string) *ErrorDetails {
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
	d := &ErrorDetails{
		Type:    raw.Type,
		Message: raw.Message,
	}
	if raw.RetryAfterSeconds > 0 {
		d.RetryAfter = time.Duration(raw.RetryAfterSeconds * float64(time.Second))
	}
	return d
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
