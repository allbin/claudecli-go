package claudecli

import (
	"encoding/json"
	"testing"
)

func TestAuthStatusResult_JSON(t *testing.T) {
	raw := `{
		"loggedIn": true,
		"authMethod": "claude.ai",
		"apiProvider": "firstParty",
		"email": "user@example.com",
		"orgId": "org-123",
		"orgName": "My Org",
		"subscriptionType": "team"
	}`

	var s AuthStatusResult
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !s.LoggedIn {
		t.Error("expected LoggedIn=true")
	}
	if s.Email != "user@example.com" {
		t.Errorf("Email = %q, want %q", s.Email, "user@example.com")
	}
	if s.OrgName != "My Org" {
		t.Errorf("OrgName = %q, want %q", s.OrgName, "My Org")
	}
	if s.SubscriptionType != "team" {
		t.Errorf("SubscriptionType = %q, want %q", s.SubscriptionType, "team")
	}
}

func TestAuthStatusResult_NotLoggedIn(t *testing.T) {
	raw := `{"loggedIn": false}`

	var s AuthStatusResult
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if s.LoggedIn {
		t.Error("expected LoggedIn=false")
	}
	if s.Email != "" {
		t.Errorf("Email = %q, want empty", s.Email)
	}
}

func TestExtractLoginURL(t *testing.T) {
	tests := []struct {
		line string
		want string
	}{
		{
			"If the browser didn't open, visit: https://claude.com/cai/oauth/authorize?code=true&client_id=abc",
			"https://claude.com/cai/oauth/authorize?code=true&client_id=abc",
		},
		{
			"If the browser didn't open, visit: https://example.com/auth  ",
			"https://example.com/auth",
		},
		{"Opening browser to sign in…", ""},
		{"random output", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := extractLoginURL(tt.line)
		if got != tt.want {
			t.Errorf("extractLoginURL(%q) = %q, want %q", tt.line, got, tt.want)
		}
	}
}

func TestAuthLoginOptions_Args(t *testing.T) {
	// Verify that login options produce the expected config.
	var cfg authLoginConfig
	opts := []AuthLoginOption{
		WithAuthMethod(AuthMethodConsole),
		WithSSO(),
		WithLoginEmail("user@example.com"),
	}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.method != AuthMethodConsole {
		t.Errorf("method = %q, want %q", cfg.method, AuthMethodConsole)
	}
	if !cfg.sso {
		t.Error("expected sso=true")
	}
	if cfg.email != "user@example.com" {
		t.Errorf("email = %q, want %q", cfg.email, "user@example.com")
	}
}

func TestAuthLoginOptions_Defaults(t *testing.T) {
	var cfg authLoginConfig
	if cfg.method != "" {
		t.Errorf("default method = %q, want empty", cfg.method)
	}
	if cfg.sso {
		t.Error("default sso should be false")
	}
	if cfg.email != "" {
		t.Errorf("default email = %q, want empty", cfg.email)
	}
}
