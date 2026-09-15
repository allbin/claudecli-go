//go:build integration

package claudecli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// effortRecorder is a reverse proxy in front of the Messages API that records
// output_config.effort from every main-loop request. It is the only way to
// see what the CLI actually sends: get_settings reports the CLI's belief, and
// nothing in the event stream carries the effort at all.
type effortRecorder struct {
	mu      sync.Mutex
	efforts []string
}

// newEffortRecorder starts the proxy and returns the ANTHROPIC_BASE_URL that
// routes a CLI through it. It forwards to the caller's own
// ANTHROPIC_BASE_URL when one is set, so a gateway setup keeps working.
func newEffortRecorder(t *testing.T) (*effortRecorder, string) {
	t.Helper()
	upstream := os.Getenv("ANTHROPIC_BASE_URL")
	if upstream == "" {
		upstream = "https://api.anthropic.com"
	}
	target, err := url.Parse(upstream)
	if err != nil {
		t.Fatalf("parse upstream %q: %v", upstream, err)
	}

	rec := &effortRecorder{}
	proxy := httputil.NewSingleHostReverseProxy(target)
	direct := proxy.Director
	proxy.Director = func(req *http.Request) {
		direct(req)
		req.Host = target.Host
		if req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Path, "/v1/messages") || req.Body == nil {
			return
		}
		body, err := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(body))
		if err != nil {
			return
		}
		var msg struct {
			Tools        []json.RawMessage `json:"tools"`
			OutputConfig struct {
				Effort string `json:"effort"`
			} `json:"output_config"`
		}
		// Title generation and other side calls carry no tools; only the main
		// loop's requests say anything about the session's effort.
		if json.Unmarshal(body, &msg) != nil || len(msg.Tools) == 0 {
			return
		}
		rec.mu.Lock()
		rec.efforts = append(rec.efforts, msg.OutputConfig.Effort)
		rec.mu.Unlock()
	}
	srv := httptest.NewServer(proxy)
	t.Cleanup(srv.Close)
	return rec, srv.URL
}

// since returns the efforts recorded from index n onwards, and the new length.
func (r *effortRecorder) since(n int) ([]string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.efforts[n:]...), len(r.efforts)
}

func (r *effortRecorder) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.efforts)
}

// drainEvents keeps the session's event channel from filling up, passing each
// event to fn when it is set.
func drainEvents(s *Session, fn func(Event)) {
	go func() {
		for ev := range s.Events() {
			if fn != nil {
				fn(ev)
			}
		}
	}()
}

// TestIntegrationSetEffortReachesAPI proves the level SetEffort returns is the
// one the CLI sends, between turns, including a reset to the default.
func TestIntegrationSetEffortReachesAPI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	rec, baseURL := newEffortRecorder(t)
	session, err := New().Connect(ctx,
		WithModel(ModelSonnet),
		WithEffort(EffortLow),
		WithWorkDir(t.TempDir()),
		WithEnv(map[string]string{"ANTHROPIC_BASE_URL": baseURL}),
	)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer session.Close()
	drainEvents(session, nil)

	mark := 0
	turn := func(label, want string) {
		t.Helper()
		if err := session.Query("Reply with exactly the word: " + label); err != nil {
			t.Fatalf("%s: Query: %v", label, err)
		}
		if _, err := session.Wait(); err != nil {
			t.Fatalf("%s: Wait: %v", label, err)
		}
		var sent []string
		sent, mark = rec.since(mark)
		if len(sent) == 0 {
			t.Fatalf("%s: no main-loop request recorded — is the proxy in the path?", label)
		}
		for _, got := range sent {
			if got != want {
				t.Errorf("%s: sent effort %q, want %q (all: %v)", label, got, want, sent)
			}
		}
	}

	turn("one", "low")

	applied, err := session.SetEffort(EffortHigh)
	if err != nil {
		t.Fatalf("SetEffort(high): %v", err)
	}
	if applied != EffortHigh {
		t.Fatalf("SetEffort(high) returned %q", applied)
	}
	turn("two", "high")

	applied, err = session.SetEffort("")
	if err != nil {
		t.Fatalf("SetEffort(\"\"): %v", err)
	}
	if applied == "" {
		t.Fatalf("SetEffort(\"\") returned %q, want the model's resolved default", applied)
	}
	turn("three", string(applied))
}

// TestIntegrationSetEffortMidTurn proves a change made while a turn is running
// applies to that turn's remaining requests, not just the next turn.
func TestIntegrationSetEffortMidTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	rec, baseURL := newEffortRecorder(t)
	session, err := New().Connect(ctx,
		WithModel(ModelSonnet),
		WithEffort(EffortLow),
		WithPermissionMode(PermissionBypass),
		WithWorkDir(t.TempDir()),
		WithEnv(map[string]string{"ANTHROPIC_BASE_URL": baseURL}),
	)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer session.Close()

	type change struct {
		applied EffortLevel
		err     error
		at      int // recorded requests when the change returned
	}
	changed := make(chan change, 1)
	var once sync.Once
	drainEvents(session, func(ev Event) {
		if _, ok := ev.(*ToolUseEvent); !ok {
			return
		}
		// The first tool call sleeps, so the change lands while the turn is
		// between requests. Run it off the event goroutine: SetEffort waits on
		// control responses the read loop delivers.
		once.Do(func() {
			go func() {
				applied, err := session.SetEffort(EffortHigh)
				changed <- change{applied: applied, err: err, at: rec.len()}
			}()
		})
	})

	prompt := "Use the Bash tool twice, one call per message, waiting for each result: " +
		"first `sleep 5; echo a`, then `echo b`. Then reply with the word done."
	if err := session.Query(prompt); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if _, err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	var c change
	select {
	case c = <-changed:
	default:
		t.Fatal("the turn made no tool call, so nothing changed mid-turn")
	}
	if c.err != nil || c.applied != EffortHigh {
		t.Fatalf("mid-turn SetEffort = %q, %v", c.applied, c.err)
	}

	before, _ := rec.since(0)
	after, _ := rec.since(c.at)
	if len(after) == 0 {
		t.Fatalf("no request followed the change (all: %v)", before)
	}
	if before[0] != "low" {
		t.Errorf("turn opened at %q, want low (all: %v)", before[0], before)
	}
	for _, got := range after {
		if got != "high" {
			t.Errorf("request after the change sent %q, want high (after: %v)", got, after)
		}
	}
}
