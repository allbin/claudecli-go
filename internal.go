package claudecli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

const maxStderrLines = 1000

// stderrRing is an O(1)-insert ring buffer for the last N stderr lines.
type stderrRing struct {
	buf  []string
	pos  int
	full bool
}

func newStderrRing(cap int) *stderrRing {
	return &stderrRing{buf: make([]string, cap)}
}

func (r *stderrRing) add(line string) {
	r.buf[r.pos] = line
	r.pos++
	if r.pos == len(r.buf) {
		r.pos = 0
		r.full = true
	}
}

// lines returns all collected lines in chronological order.
func (r *stderrRing) lines() []string {
	if !r.full {
		return r.buf[:r.pos]
	}
	out := make([]string, len(r.buf))
	copy(out, r.buf[r.pos:])
	copy(out[len(r.buf)-r.pos:], r.buf[:r.pos])
	return out
}

// lineCaptureReader wraps an io.Reader and records complete lines (delimited
// by '\n') into a ring buffer as bytes pass through. Used to capture raw JSONL
// from stdout for error diagnostics without an extra scanner goroutine.
type lineCaptureReader struct {
	r    io.Reader
	ring *stderrRing
	buf  []byte
}

func (r *lineCaptureReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.buf = append(r.buf, p[:n]...)
		for {
			idx := bytes.IndexByte(r.buf, '\n')
			if idx < 0 {
				break
			}
			r.ring.add(string(r.buf[:idx]))
			r.buf = r.buf[idx+1:]
		}
	}
	return n, err
}

// flush captures any remaining partial line in the buffer.
func (r *lineCaptureReader) flush() {
	if len(r.buf) > 0 {
		r.ring.add(string(r.buf))
		r.buf = nil
	}
}

func scanStderr(ctx context.Context, proc *Process, events chan<- Event, callback func(string)) (*stderrRing, <-chan struct{}) {
	ring := newStderrRing(maxStderrLines)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				select {
				case events <- &ErrorEvent{
					Err:   fmt.Errorf("stderr goroutine panic: %v", r),
					Fatal: true,
				}:
				case <-ctx.Done():
				}
			}
		}()
		scanner := bufio.NewScanner(proc.Stderr)
		for scanner.Scan() {
			line := scanner.Text()
			ring.add(line)
			if callback != nil {
				callback(line)
			}
			select {
			case events <- &StderrEvent{Content: line}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ring, done
}

func processExitError(err error, stderr string) *Error {
	details := parseErrorDetails(stderr)
	var msg string
	var class error
	if details != nil {
		msg = details.message
		class = classifyError(details)
	}
	if msg == "" {
		msg = inferErrorMessage(stderr)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		if msg == "" {
			msg = err.Error()
		}
		return &Error{ExitCode: -1, Stderr: stderr, Message: msg, class: class}
	}
	return &Error{
		ExitCode: exitErr.ExitCode(),
		Stderr:   stderr,
		Message:  msg,
		class:    class,
	}
}

// inferErrorMessage extracts a human-readable summary from unstructured stderr.
// Matches common OS-level patterns first, then falls back to the last
// non-empty, non-JSON line.
func inferErrorMessage(stderr string) string {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "command not found"):
		return "claude binary not found (is it installed and in PATH?)"
	case strings.Contains(lower, "no such file or directory") && !strings.Contains(lower, "{"):
		return "file or directory not found (check working directory and binary path)"
	case strings.Contains(lower, "permission denied"):
		return "permission denied running claude binary"
	case strings.Contains(lower, "enoent"):
		return "file or directory not found (ENOENT)"
	case strings.Contains(lower, "eacces"):
		return "permission denied (EACCES)"
	}

	// Use last non-empty, non-JSON stderr line as the most relevant context.
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "{") {
			continue
		}
		if len(line) > 200 {
			return line[:200] + "..."
		}
		return line
	}
	return ""
}

// stripCodeFence removes surrounding markdown code fences from text.
// Handles ```json\n...\n```, ```\n...\n```, and leading/trailing whitespace.
// Only matches exactly three backticks optionally followed by a language tag
// (letters/digits only) — four+ backticks or non-alphanumeric suffixes are ignored.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	lines := strings.SplitN(s, "\n", 2)
	if len(lines) < 2 {
		return s
	}
	first := strings.TrimSpace(lines[0])
	if !isOpeningFence(first) {
		return s
	}
	// Find closing fence (may not be last line if model appends commentary)
	rest := s[strings.Index(s, "\n")+1:]
	fenceIdx := -1
	pos := 0
	for {
		nl := strings.Index(rest[pos:], "\n")
		var line string
		if nl < 0 {
			line = rest[pos:]
		} else {
			line = rest[pos : pos+nl]
		}
		if strings.TrimSpace(line) == "```" {
			fenceIdx = pos
			break
		}
		if nl < 0 {
			break
		}
		pos += nl + 1
	}
	if fenceIdx < 0 {
		return s
	}
	// Extract content between fences
	inner := rest[:fenceIdx]
	return strings.TrimSpace(inner)
}

// isOpeningFence returns true for exactly ``` or ```<alphanum lang tag>.
func isOpeningFence(line string) bool {
	if !strings.HasPrefix(line, "```") {
		return false
	}
	tag := line[3:]
	if tag == "" {
		return true
	}
	// Reject 4+ backticks or non-alphanumeric suffixes
	for _, r := range tag {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}
