package claudecli

import (
	"context"
	"encoding/json"
	"testing"
)

// Trimmed from a live get_context_usage response (Claude CLI 2.1.235). The
// full payload also carries gridRows, mcpTools, memoryFiles, agents, skills,
// messageBreakdown and apiUsage; those stay reachable through Raw.
const contextUsageResponse = `{"model":"claude-opus-4-6","totalTokens":41234,"maxTokens":200000,"rawMaxTokens":1000000,"autocompactSource":"settings","percentage":21,"isAutoCompactEnabled":true,"autoCompactThreshold":184000,"categories":[{"name":"System prompt","tokens":3120,"color":"blue"},{"name":"Tools","tokens":18400,"color":"green","isDeferred":true},{"name":"Messages","tokens":19714,"color":"red"}],"mcpTools":[{"name":"mcp__x__y","serverName":"x","tokens":120}]}`

func TestQueryContextUsage(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)
		msg := sim.respondSuccessWithBody(t, contextUsageResponse)
		request := msg["request"].(map[string]any)
		if request["subtype"] != "get_context_usage" {
			t.Errorf("expected get_context_usage, got %v", request["subtype"])
		}
		sim.sendResult()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	usage, err := session.QueryContextUsage()
	if err != nil {
		t.Fatal(err)
	}

	if usage.Model != "claude-opus-4-6" {
		t.Errorf("Model = %q", usage.Model)
	}
	if usage.TotalTokens != 41234 || usage.MaxTokens != 200000 {
		t.Errorf("tokens = %d/%d", usage.TotalTokens, usage.MaxTokens)
	}
	// A 1M-window model compacting against a 200K policy window: the two
	// limits genuinely differ and must not be collapsed.
	if usage.RawMaxTokens != 1000000 {
		t.Errorf("RawMaxTokens = %d, want the model's hard limit", usage.RawMaxTokens)
	}
	if !usage.IsAutoCompactEnabled || usage.AutoCompactThreshold != 184000 {
		t.Errorf("autocompact = %v/%d", usage.IsAutoCompactEnabled, usage.AutoCompactThreshold)
	}
	if usage.AutocompactSource != "settings" {
		t.Errorf("AutocompactSource = %q", usage.AutocompactSource)
	}
	if len(usage.Categories) != 3 {
		t.Fatalf("Categories = %d, want 3", len(usage.Categories))
	}
	if !usage.Categories[1].IsDeferred {
		t.Error("deferred category not marked deferred")
	}
	if got := usage.Remaining(); got != 200000-41234 {
		t.Errorf("Remaining() = %d", got)
	}

	// Fields the typed struct deliberately omits must survive in Raw.
	var full map[string]json.RawMessage
	if err := json.Unmarshal(usage.Raw, &full); err != nil {
		t.Fatalf("Raw is not valid JSON: %v", err)
	}
	if _, ok := full["mcpTools"]; !ok {
		t.Error("Raw dropped unmodelled fields")
	}

	if _, err := session.Wait(); err != nil {
		t.Fatal(err)
	}
}

// Over-limit sessions report TotalTokens above the window; Remaining must
// clamp rather than go negative.
func TestContextUsageRemainingClampsAtZero(t *testing.T) {
	u := &ContextUsage{TotalTokens: 210000, MaxTokens: 200000}
	if got := u.Remaining(); got != 0 {
		t.Errorf("Remaining() = %d, want 0", got)
	}
}
