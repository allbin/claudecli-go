package claudecli

import (
	"context"
	"encoding/json"
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
