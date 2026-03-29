// Command capture runs the Claude CLI and saves raw stdout/stderr for analysis.
// It bypasses the library's event parsing to capture the full unfiltered JSONL stream.
//
// Usage:
//
//	go run ./cmd/capture -prompt "Use the Agent tool to read go.mod and tell me the module name"
//	go run ./cmd/capture -analyze tmp/raw-stdout.jsonl
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"

	claudecli "github.com/allbin/claudecli-go"
)

func main() {
	prompt := flag.String("prompt", "Use the Agent tool to read the file go.mod and tell me the module name", "prompt to send to Claude CLI")
	outDir := flag.String("out", "tmp", "output directory for captured files")
	timeout := flag.Duration("timeout", 120*time.Second, "session timeout")
	analyze := flag.String("analyze", "", "path to JSONL file to analyze instead of capturing")
	flag.Parse()

	if *analyze != "" {
		if err := analyzeJSONL(*analyze); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := capture(*prompt, *outDir, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func capture(prompt, outDir string, timeout time.Duration) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	ctx, cancel = context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", outDir, err)
	}

	stdoutPath := filepath.Join(outDir, "raw-stdout.jsonl")
	stderrPath := filepath.Join(outDir, "raw-stderr.log")

	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", stdoutPath, err)
	}
	defer stdoutFile.Close()

	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", stderrPath, err)
	}
	defer stderrFile.Close()

	args := []string{
		"--print",
		"--verbose",
		"--output-format", "stream-json",
		"--max-turns", "3",
		"-p", prompt,
	}

	fmt.Fprintf(os.Stderr, "running: claude %s\n", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, "claude", args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start claude: %w", err)
	}

	// Tee stderr to file + terminal.
	go func() {
		w := io.MultiWriter(stderrFile, os.Stderr)
		io.Copy(w, stderr)
	}()

	// Tee stdout to file, then pipe through ParseEvents.
	pr, pw := io.Pipe()
	go func() {
		w := io.MultiWriter(stdoutFile, pw)
		io.Copy(w, stdout)
		pw.Close()
	}()

	ch := make(chan claudecli.Event, 256)
	go func() {
		claudecli.ParseEvents(pr, ch)
		close(ch)
	}()

	typeCounts := map[string]int{}
	var unknowns []*claudecli.UnknownEvent

	for ev := range ch {
		typeName := fmt.Sprintf("%T", ev)
		typeCounts[typeName]++

		if u, ok := ev.(*claudecli.UnknownEvent); ok {
			unknowns = append(unknowns, u)
			fmt.Fprintf(os.Stderr, ">>> UNKNOWN EVENT: type=%s raw=%s\n", u.Type, string(u.Raw))
		}

		// Print progress.
		switch e := ev.(type) {
		case *claudecli.TextEvent:
			fmt.Print(e.Content)
		case *claudecli.ToolUseEvent:
			fmt.Fprintf(os.Stderr, "[tool: %s]\n", e.Name)
		case *claudecli.ResultEvent:
			fmt.Fprintf(os.Stderr, "\n[result: cost=$%.4f tokens=%d/%d]\n", e.CostUSD, e.Usage.InputTokens, e.Usage.OutputTokens)
		}
	}

	cmd.Wait()

	// Print summary.
	fmt.Fprintf(os.Stderr, "\n=== Event Type Summary ===\n")
	var types []string
	for t := range typeCounts {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		fmt.Fprintf(os.Stderr, "  %-35s %d\n", t, typeCounts[t])
	}

	fmt.Fprintf(os.Stderr, "\nUnknown events: %d\n", len(unknowns))
	for _, u := range unknowns {
		fmt.Fprintf(os.Stderr, "  type=%s\n  raw=%s\n\n", u.Type, string(u.Raw))
	}

	fmt.Fprintf(os.Stderr, "\nOutput saved to:\n  stdout: %s\n  stderr: %s\n", stdoutPath, stderrPath)
	return nil
}

func analyzeJSONL(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	typeCounts := map[string]int{}
	var unknownLines []string

	// First pass: count raw JSON types.
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var obj struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			fmt.Fprintf(os.Stderr, "bad json: %s\n", line)
			continue
		}
		typeCounts[obj.Type]++

		known := map[string]bool{
			"system": true, "assistant": true, "result": true,
			"rate_limit_event": true, "control_request": true,
			"control_response": true, "stream_event": true, "error": true,
		}
		if !known[obj.Type] {
			unknownLines = append(unknownLines, line)
		}
	}

	fmt.Println("=== Raw JSON Type Counts ===")
	var types []string
	for t := range typeCounts {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		fmt.Printf("  %-25s %d\n", t, typeCounts[t])
	}

	fmt.Printf("\nUnrecognized types: %d\n", len(unknownLines))
	for _, line := range unknownLines {
		fmt.Printf("  %s\n", line)
	}

	// Second pass: feed through ParseEvents to confirm.
	f.Seek(0, 0)
	ch := make(chan claudecli.Event, 256)
	go func() {
		claudecli.ParseEvents(f, ch)
		close(ch)
	}()

	parsedCounts := map[string]int{}
	var parsedUnknowns []*claudecli.UnknownEvent
	for ev := range ch {
		parsedCounts[fmt.Sprintf("%T", ev)]++
		if u, ok := ev.(*claudecli.UnknownEvent); ok {
			parsedUnknowns = append(parsedUnknowns, u)
		}
	}

	fmt.Println("\n=== ParseEvents Output ===")
	types = types[:0]
	for t := range parsedCounts {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		fmt.Printf("  %-40s %d\n", t, parsedCounts[t])
	}

	fmt.Printf("\nUnknownEvent instances: %d\n", len(parsedUnknowns))
	for _, u := range parsedUnknowns {
		fmt.Printf("  type=%s\n  raw=%s\n\n", u.Type, string(u.Raw))
	}

	return nil
}
