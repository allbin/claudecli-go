package claudecli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// BlockingResult contains the output from a non-streaming CLI invocation
// using --output-format json. Unlike streaming, this returns only the final
// result with no intermediate events.
type BlockingResult struct {
	Text             string
	StructuredOutput json.RawMessage
	Subtype          string
	SessionID        string
	CostUSD          float64
	Duration         time.Duration
	NumTurns         int
	IsError          bool
	Usage            Usage
	Stderr           string
}

// RunBlocking runs a prompt with --output-format json (no streaming).
// Simpler and more reliable than streaming when intermediate events aren't needed.
// When WithJSONSchema is set, the validated output is available in StructuredOutput.
func (c *Client) RunBlocking(ctx context.Context, prompt string, opts ...Option) (*BlockingResult, error) {
	resolved := resolveOptions(c.defaults, opts)
	args := resolved.buildBlockingArgs()

	proc, err := c.executor.Start(ctx, &StartConfig{
		Args:                    args,
		Stdin:                   strings.NewReader(prompt),
		Env:                     resolved.env,
		WorkDir:                 resolved.workDir,
		EnableFileCheckpointing: resolved.enableFileCheckpointing,
		SkipVersionCheck:        resolved.skipVersionCheck,
	})
	if err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	stdout, readErr := io.ReadAll(proc.Stdout)
	stderrOut, _ := io.ReadAll(proc.Stderr)

	if waitErr := proc.Wait(); waitErr != nil {
		return nil, blockingProcessError(waitErr, stderrOut)
	}
	if readErr != nil {
		return nil, fmt.Errorf("read stdout: %w", readErr)
	}

	stderrStr := strings.TrimSpace(string(stderrOut))
	if stderrStr != "" && resolved.stderrCallback != nil {
		for _, line := range strings.Split(stderrStr, "\n") {
			resolved.stderrCallback(line)
		}
	}

	result, err := parseBlockingJSON(stdout)
	if err != nil {
		return nil, err
	}
	result.Stderr = stderrStr
	return result, nil
}

// RunBlockingJSON runs a prompt with --output-format json and unmarshals the result into T.
// When WithJSONSchema is set, parses the schema-validated structured_output field.
// Otherwise, parses the text result (with code fence stripping).
func RunBlockingJSON[T any](ctx context.Context, c *Client, prompt string, opts ...Option) (T, *BlockingResult, error) {
	var zero T
	result, err := c.RunBlocking(ctx, prompt, opts...)
	if err != nil {
		return zero, nil, err
	}

	source := pickJSONSource(result)
	if err := json.Unmarshal(source, &zero); err != nil {
		return zero, result, &UnmarshalError{Err: err, RawText: string(source)}
	}
	return zero, result, nil
}

// pickJSONSource returns structured_output if available, otherwise the text result
// with code fences stripped.
func pickJSONSource(result *BlockingResult) []byte {
	if len(result.StructuredOutput) > 0 {
		return result.StructuredOutput
	}
	return []byte(stripCodeFence(result.Text))
}

func blockingProcessError(err error, stderr []byte) error {
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		return err
	}
	s := strings.TrimSpace(string(stderr))
	return &Error{
		ExitCode: exitErr.ExitCode(),
		Stderr:   s,
		Details:  parseErrorDetails(s),
	}
}

// rawBlockingResult is the JSON structure returned by --output-format json.
type rawBlockingResult struct {
	Type             string          `json:"type"`
	Subtype          string          `json:"subtype"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output,omitempty"`
	SessionID        string          `json:"session_id"`
	CostUSD          float64         `json:"total_cost_usd"`
	DurationMS       float64         `json:"duration_ms"`
	NumTurns         int             `json:"num_turns"`
	IsError          bool            `json:"is_error"`
	Usage            rawUsage        `json:"usage"`
}

func parseBlockingJSON(data []byte) (*BlockingResult, error) {
	data = bytes.TrimSpace(data)

	// Claude CLI may return a JSON array instead of a single object.
	// When it does, find the result element (last with type "result",
	// or just the last element).
	if len(data) > 0 && data[0] == '[' {
		var arr []rawBlockingResult
		if err := json.Unmarshal(data, &arr); err != nil {
			return nil, fmt.Errorf("unmarshal blocking result array: %w", err)
		}
		if len(arr) == 0 {
			return nil, fmt.Errorf("empty blocking result array")
		}
		// Prefer the "result" typed element; fall back to last.
		idx := len(arr) - 1
		for i := len(arr) - 1; i >= 0; i-- {
			if arr[i].Type == "result" {
				idx = i
				break
			}
		}
		return rawToBlocking(&arr[idx]), nil
	}

	var raw rawBlockingResult
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal blocking result: %w", err)
	}

	return rawToBlocking(&raw), nil
}

func rawToBlocking(raw *rawBlockingResult) *BlockingResult {
	return &BlockingResult{
		Text:             raw.Result,
		StructuredOutput: raw.StructuredOutput,
		Subtype:          raw.Subtype,
		SessionID:        raw.SessionID,
		CostUSD:          raw.CostUSD,
		Duration:         time.Duration(raw.DurationMS) * time.Millisecond,
		NumTurns:         raw.NumTurns,
		IsError:          raw.IsError,
		Usage: Usage{
			InputTokens:       raw.Usage.InputTokens,
			OutputTokens:      raw.Usage.OutputTokens,
			CacheReadTokens:   raw.Usage.CacheReadInputTokens,
			CacheCreateTokens: raw.Usage.CacheCreationInputTokens,
		},
	}
}
