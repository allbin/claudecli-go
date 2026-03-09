package claudecli

import (
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
}

// RunBlocking runs a prompt with --output-format json (no streaming).
// Simpler and more reliable than streaming when intermediate events aren't needed.
// When WithJSONSchema is set, the validated output is available in StructuredOutput.
func (c *Client) RunBlocking(ctx context.Context, prompt string, opts ...Option) (*BlockingResult, error) {
	resolved := resolveOptions(c.defaults, opts)
	args := resolved.buildBlockingArgs()

	proc, err := c.executor.Start(ctx, &StartConfig{
		Args:    args,
		Stdin:   strings.NewReader(prompt),
		Env:     resolved.env,
		WorkDir: resolved.workDir,
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

	return parseBlockingJSON(stdout)
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
	return &Error{
		ExitCode: exitErr.ExitCode(),
		Stderr:   strings.TrimSpace(string(stderr)),
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
	var raw rawBlockingResult
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal blocking result: %w", err)
	}

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
	}, nil
}
