package claudecli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestQuerySettings(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	const body = `{"effective":{"permissions":{"allow":["Bash(echo:*)"],"deny":["Bash(rm:*)"]}},"sources":{"flagSettings":null,"userSettings":{}},"applied":{"effort":"high","model":"claude-opus-4-6"}}`

	go func() {
		sim.handleInitAndReady(t)
		msg := sim.respondSuccessWithBody(t, body)
		request := msg["request"].(map[string]any)
		if request["subtype"] != "get_settings" {
			t.Errorf("expected get_settings, got %v", request["subtype"])
		}
		sim.sendResult()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	snap, err := session.QuerySettings()
	if err != nil {
		t.Fatal(err)
	}

	var eff struct {
		Permissions PermissionRules `json:"permissions"`
	}
	if err := json.Unmarshal(snap.Effective, &eff); err != nil {
		t.Fatalf("Effective not decodable: %v", err)
	}
	if len(eff.Permissions.Deny) != 1 || eff.Permissions.Deny[0] != "Bash(rm:*)" {
		t.Errorf("effective permissions = %+v", eff.Permissions)
	}

	// applied is where the resolved effort lives, and is distinct from the
	// on-disk merge in effective.
	var applied struct {
		Effort string `json:"effort"`
	}
	if err := json.Unmarshal(snap.Applied, &applied); err != nil {
		t.Fatalf("Applied not decodable: %v", err)
	}
	if applied.Effort != "high" {
		t.Errorf("applied.effort = %q", applied.Effort)
	}

	if _, err := session.Wait(); err != nil {
		t.Fatal(err)
	}
}

// The wire shape matters here: the CLI expects the payload nested under a
// "settings" key, and gets the rules verbatim.
func TestSetPermissionRulesWireShape(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	done := make(chan map[string]any, 1)
	go func() {
		sim.handleInitAndReady(t)
		msg := sim.respondSuccess(t)
		done <- msg["request"].(map[string]any)
		sim.sendResult()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	err = session.SetPermissionRules(PermissionRules{
		Allow: []string{"Bash(echo probe:*)"},
		Deny:  []string{"Bash(rm:*)"},
	})
	if err != nil {
		t.Fatal(err)
	}

	request := <-done
	if request["subtype"] != "apply_flag_settings" {
		t.Fatalf("subtype = %v, want apply_flag_settings", request["subtype"])
	}
	settings, ok := request["settings"].(map[string]any)
	if !ok {
		t.Fatalf("settings missing or wrong type: %#v", request["settings"])
	}
	perms, ok := settings["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("permissions missing: %#v", settings)
	}
	allow, _ := perms["allow"].([]any)
	if len(allow) != 1 || allow[0] != "Bash(echo probe:*)" {
		t.Errorf("allow = %#v", perms["allow"])
	}
	// Ask was not set, so it must be omitted rather than sent as null —
	// an explicit null would clear rules the caller never mentioned.
	if _, present := perms["ask"]; present {
		t.Errorf("unset Ask was serialized: %#v", perms)
	}

	if _, err := session.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyFlagSettingsRejectsNil(t *testing.T) {
	var s Session
	if err := s.ApplyFlagSettings(nil); err == nil {
		t.Error("nil settings accepted; want error")
	}
}

// setEffortSim answers SetEffort's two control requests — apply_flag_settings,
// then get_settings with the given applied body — and hands back the apply
// request for inspection.
func setEffortSim(t *testing.T, sim *sessionSim, applied string) <-chan map[string]any {
	t.Helper()
	requests := make(chan map[string]any, 1)
	go func() {
		sim.handleInitAndReady(t)
		apply := sim.respondSuccess(t)
		requests <- apply["request"].(map[string]any)
		get := sim.respondSuccessWithBody(t, `{"effective":{},"sources":{},"applied":`+applied+`}`)
		if sub := get["request"].(map[string]any)["subtype"]; sub != "get_settings" {
			t.Errorf("second request = %v, want get_settings", sub)
		}
		sim.sendResult()
	}()
	return requests
}

func TestSetEffortWireShapeAndReadBack(t *testing.T) {
	sim := newSessionSim()
	requests := setEffortSim(t, sim, `{"model":"claude-opus-5","effort":"high"}`)

	session, err := NewWithExecutor(sim.bidi).Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	got, err := session.SetEffort(EffortHigh)
	if err != nil {
		t.Fatal(err)
	}
	if got != EffortHigh {
		t.Errorf("SetEffort returned %q, want %q", got, EffortHigh)
	}

	request := <-requests
	if request["subtype"] != "apply_flag_settings" {
		t.Fatalf("subtype = %v, want apply_flag_settings", request["subtype"])
	}
	settings, _ := request["settings"].(map[string]any)
	if settings["effortLevel"] != "high" || len(settings) != 1 {
		t.Errorf("settings = %#v, want exactly effortLevel=high", settings)
	}

	if _, err := session.Wait(); err != nil {
		t.Fatal(err)
	}
}

// An empty level must reach the CLI as an explicit null: that is what clears
// the session's level. Omitting the key would change nothing.
func TestSetEffortEmptySendsNull(t *testing.T) {
	sim := newSessionSim()
	requests := setEffortSim(t, sim, `{"model":"claude-opus-5","effort":"high"}`)

	session, err := NewWithExecutor(sim.bidi).Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	got, err := session.SetEffort("")
	if err != nil {
		t.Fatal(err)
	}
	if got != EffortHigh {
		t.Errorf("SetEffort(\"\") returned %q, want the resolved default %q", got, EffortHigh)
	}

	settings, _ := (<-requests)["settings"].(map[string]any)
	value, present := settings["effortLevel"]
	if !present || value != nil {
		t.Errorf("settings = %#v, want effortLevel present and null", settings)
	}

	if _, err := session.Wait(); err != nil {
		t.Fatal(err)
	}
}

// The CLI answers success even when it did not take the level (an unknown
// value, a cap), so the return value has to come from the read-back, not the
// request.
func TestSetEffortReturnsResolvedLevelNotRequested(t *testing.T) {
	sim := newSessionSim()
	setEffortSim(t, sim, `{"model":"claude-opus-5","effort":"max"}`)

	session, err := NewWithExecutor(sim.bidi).Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	got, err := session.SetEffort(EffortLevel("bogus"))
	if err != nil {
		t.Fatal(err)
	}
	if got != EffortMax {
		t.Errorf("SetEffort returned %q, want the level still in force %q", got, EffortMax)
	}

	if _, err := session.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestSetEffortModelWithoutEffort(t *testing.T) {
	sim := newSessionSim()
	setEffortSim(t, sim, `{"model":"claude-haiku-4-5-20251001","effort":null}`)

	session, err := NewWithExecutor(sim.bidi).Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	got, err := session.SetEffort(EffortHigh)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("SetEffort returned %q, want empty for a model without effort", got)
	}

	if _, err := session.Wait(); err != nil {
		t.Fatal(err)
	}
}

// A rejected apply must surface as an error without a read-back: reporting the
// old level as if it were the outcome would hide the failure.
func TestSetEffortApplyErrorSkipsReadBack(t *testing.T) {
	sim := newSessionSim()
	go func() {
		sim.handleInitAndReady(t)
		sim.respondError(t, "Unsupported control request subtype: apply_flag_settings")
		sim.sendResult()
	}()

	session, err := NewWithExecutor(sim.bidi).Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	got, err := session.SetEffort(EffortHigh)
	if err == nil {
		t.Fatalf("SetEffort succeeded with %q; want the apply error", got)
	}
	if !strings.Contains(err.Error(), "Unsupported control request subtype") {
		t.Errorf("error = %v, want the CLI's message", err)
	}

	if _, err := session.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsSnapshotAppliedEffort(t *testing.T) {
	tests := []struct {
		name    string
		applied string
		want    EffortLevel
	}{
		{"level", `{"effort":"xhigh"}`, EffortXHigh},
		{"null", `{"effort":null}`, ""},
		{"absent", `{"model":"claude-opus-5"}`, ""},
		{"no applied view", ``, ""},
		{"undecodable", `[1,2]`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := &SettingsSnapshot{Applied: json.RawMessage(tt.applied)}
			if got := snap.AppliedEffort(); got != tt.want {
				t.Errorf("AppliedEffort() = %q, want %q", got, tt.want)
			}
		})
	}
}
