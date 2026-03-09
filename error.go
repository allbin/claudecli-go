package claudecli

import "fmt"

// Error represents a CLI process failure with context.
type Error struct {
	ExitCode int
	Stderr   string
	Message  string
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
