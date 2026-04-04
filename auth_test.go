package claudecli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
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
		name string
		line string
		want string
	}{
		{
			"primary prefix",
			"If the browser didn't open, visit: https://claude.com/cai/oauth/authorize?code=true&client_id=abc",
			"https://claude.com/cai/oauth/authorize?code=true&client_id=abc",
		},
		{
			"primary prefix trailing space",
			"If the browser didn't open, visit: https://example.com/auth  ",
			"https://example.com/auth",
		},
		{
			"fallback visit keyword",
			"Please visit https://claude.ai/oauth/authorize?client_id=abc to continue",
			"https://claude.ai/oauth/authorize?client_id=abc",
		},
		{
			"fallback open keyword",
			"Open https://claude.ai/oauth/authorize?state=xyz in your browser",
			"https://claude.ai/oauth/authorize?state=xyz",
		},
		{
			"fallback OAuth URL without keywords",
			"https://claude.ai/oauth/authorize?client_id=abc&state=xyz",
			"https://claude.ai/oauth/authorize?client_id=abc&state=xyz",
		},
		{
			"no match",
			"Opening browser to sign in…",
			"",
		},
		{"random output", "random output", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractLoginURL(tt.line)
			if got != tt.want {
				t.Errorf("extractLoginURL(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
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

func TestAuthLoginOptions_NoBrowser(t *testing.T) {
	var cfg authLoginConfig
	WithNoBrowser()(&cfg)
	if !cfg.noBrowser {
		t.Error("expected noBrowser=true")
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
	if cfg.noBrowser {
		t.Error("default noBrowser should be false")
	}
}

func TestExtractCallbackPort(t *testing.T) {
	tests := []struct {
		name    string
		autoURL string
		want    int
		wantErr bool
	}{
		{
			name:    "valid auto URL",
			autoURL: "https://claude.ai/oauth/authorize?client_id=abc&redirect_uri=http%3A%2F%2Flocalhost%3A12345%2Fcallback&state=xyz",
			want:    12345,
		},
		{
			name:    "high port",
			autoURL: "https://claude.ai/oauth/authorize?redirect_uri=http%3A%2F%2Flocalhost%3A59876%2Fcallback",
			want:    59876,
		},
		{
			name:    "missing redirect_uri",
			autoURL: "https://claude.ai/oauth/authorize?client_id=abc",
			wantErr: true,
		},
		{
			name:    "redirect_uri without port",
			autoURL: "https://claude.ai/oauth/authorize?redirect_uri=http%3A%2F%2Flocalhost%2Fcallback",
			wantErr: true,
		},
		{
			name:    "invalid URL",
			autoURL: "://not-a-url",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractCallbackPort(tt.autoURL)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got port=%d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("port = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestWaitForAutoURL(t *testing.T) {
	t.Run("file exists immediately", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "url")
		os.WriteFile(f, []byte("https://example.com/auth"), 0644)

		got, err := waitForAutoURL(f, time.Second)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://example.com/auth" {
			t.Errorf("got %q, want %q", got, "https://example.com/auth")
		}
	})

	t.Run("file appears after delay", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "url")

		go func() {
			time.Sleep(100 * time.Millisecond)
			os.WriteFile(f, []byte("https://delayed.com"), 0644)
		}()

		got, err := waitForAutoURL(f, 2*time.Second)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://delayed.com" {
			t.Errorf("got %q, want %q", got, "https://delayed.com")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "nonexistent")

		_, err := waitForAutoURL(f, 100*time.Millisecond)
		if err == nil {
			t.Error("expected timeout error")
		}
	})
}

func TestSubmitCode(t *testing.T) {
	t.Run("CODE#STATE format", func(t *testing.T) {
		var mu sync.Mutex
		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			gotPath = r.URL.RequestURI()
			mu.Unlock()
			w.WriteHeader(200)
		}))
		defer srv.Close()

		u, _ := url.Parse(srv.URL)
		port, _ := strconv.Atoi(u.Port())

		lp := &LoginProcess{
			URL:          "https://claude.ai/oauth/authorize?state=s",
			callbackPort: port,
		}

		if err := lp.SubmitCode("mycode#mystate"); err != nil {
			t.Fatalf("SubmitCode: %v", err)
		}

		mu.Lock()
		defer mu.Unlock()
		want := "/callback?code=mycode&state=mystate"
		if gotPath != want {
			t.Errorf("request path = %q, want %q", gotPath, want)
		}
	})

	t.Run("full redirect URL", func(t *testing.T) {
		var mu sync.Mutex
		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			gotPath = r.URL.RequestURI()
			mu.Unlock()
			w.WriteHeader(200)
		}))
		defer srv.Close()

		u, _ := url.Parse(srv.URL)
		port, _ := strconv.Atoi(u.Port())

		lp := &LoginProcess{
			URL:          "https://claude.ai/oauth/authorize?state=s",
			callbackPort: port,
		}

		redirectURL := fmt.Sprintf("http://localhost:%d/callback?code=fromurl&state=urlstate", port)
		if err := lp.SubmitCode(redirectURL); err != nil {
			t.Fatalf("SubmitCode: %v", err)
		}

		mu.Lock()
		defer mu.Unlock()
		want := "/callback?code=fromurl&state=urlstate"
		if gotPath != want {
			t.Errorf("request path = %q, want %q", gotPath, want)
		}
	})

	t.Run("no port returns error", func(t *testing.T) {
		lp := &LoginProcess{URL: "https://example.com"}
		err := lp.SubmitCode("code#state")
		if err == nil {
			t.Error("expected error for zero callbackPort")
		}
	})

	t.Run("invalid format returns error", func(t *testing.T) {
		lp := &LoginProcess{URL: "https://example.com", callbackPort: 9999}
		err := lp.SubmitCode("justcode")
		if err == nil {
			t.Error("expected error for bare code without state")
		}
	})

	t.Run("non-200 returns error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(400)
			w.Write([]byte("bad request"))
		}))
		defer srv.Close()

		u, _ := url.Parse(srv.URL)
		port, _ := strconv.Atoi(u.Port())

		lp := &LoginProcess{
			URL:          "https://claude.ai/oauth/authorize?state=s",
			callbackPort: port,
		}
		err := lp.SubmitCode("code#state")
		if err == nil {
			t.Error("expected error for non-200 response")
		}
	})
}

func TestParseAuthStatus(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		cmdErr     error
		wantStatus AuthState
		wantLogin  bool
		wantEmail  string
	}{
		{
			name:       "JSON loggedIn true",
			output:     `{"loggedIn": true, "email": "user@example.com", "authMethod": "claude.ai"}`,
			wantStatus: AuthStateAuthenticated,
			wantLogin:  true,
			wantEmail:  "user@example.com",
		},
		{
			name:       "JSON loggedIn false",
			output:     `{"loggedIn": false}`,
			wantStatus: AuthStateUnauthenticated,
		},
		{
			name:       "JSON isAuthenticated true",
			output:     `{"isAuthenticated": true, "email": "alt@example.com"}`,
			wantStatus: AuthStateAuthenticated,
			wantLogin:  true,
			wantEmail:  "alt@example.com",
		},
		{
			name:       "JSON no auth marker",
			output:     `{"version": "1.0"}`,
			wantStatus: AuthStateUnknown,
		},
		{
			name:       "text not logged in",
			output:     "Error: Not logged in. Run `claude login` to authenticate.",
			cmdErr:     fmt.Errorf("exit status 1"),
			wantStatus: AuthStateUnauthenticated,
		},
		{
			name:       "text login required",
			output:     "Login required to continue",
			cmdErr:     fmt.Errorf("exit status 1"),
			wantStatus: AuthStateUnauthenticated,
		},
		{
			name:       "text authentication required",
			output:     "Authentication required",
			cmdErr:     fmt.Errorf("exit status 1"),
			wantStatus: AuthStateUnauthenticated,
		},
		{
			name:       "text run claude login",
			output:     "Please run claude login first",
			cmdErr:     fmt.Errorf("exit status 1"),
			wantStatus: AuthStateUnauthenticated,
		},
		{
			name:       "unknown command",
			output:     "error: unknown command 'auth'",
			cmdErr:     fmt.Errorf("exit status 1"),
			wantStatus: AuthStateUnknown,
		},
		{
			name:       "unexpected argument",
			output:     "error: unexpected argument '--json'",
			cmdErr:     fmt.Errorf("exit status 1"),
			wantStatus: AuthStateUnknown,
		},
		{
			name:       "exit 0 non-JSON",
			output:     "Logged in as user@example.com",
			wantStatus: AuthStateAuthenticated,
			wantLogin:  true,
		},
		{
			name:       "exit non-zero unrecognized output",
			output:     "something unexpected happened",
			cmdErr:     fmt.Errorf("exit status 1"),
			wantStatus: AuthStateUnknown,
		},
		{
			name:       "JSON with subscription_type key",
			output:     `{"loggedIn": true, "subscription_type": "maxplan"}`,
			wantStatus: AuthStateAuthenticated,
			wantLogin:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseAuthStatus(tt.output, tt.cmdErr)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", result.Status, tt.wantStatus)
			}
			if result.LoggedIn != tt.wantLogin {
				t.Errorf("LoggedIn = %v, want %v", result.LoggedIn, tt.wantLogin)
			}
			if tt.wantEmail != "" && result.Email != tt.wantEmail {
				t.Errorf("Email = %q, want %q", result.Email, tt.wantEmail)
			}
		})
	}
}

func TestParseAuthStatus_SubscriptionType(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{"subscriptionType", `{"loggedIn":true,"subscriptionType":"maxplan"}`, "maxplan"},
		{"subscription_type", `{"loggedIn":true,"subscription_type":"pro"}`, "pro"},
		{"plan key", `{"loggedIn":true,"plan":"team"}`, "team"},
		{"tier key", `{"loggedIn":true,"tier":"enterprise"}`, "enterprise"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseAuthStatus(tt.output, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.SubscriptionType != tt.want {
				t.Errorf("SubscriptionType = %q, want %q", result.SubscriptionType, tt.want)
			}
		})
	}
}

func TestExtractFirstHTTPS(t *testing.T) {
	tests := []struct {
		line string
		want string
	}{
		{"visit https://example.com/auth to continue", "https://example.com/auth"},
		{"https://example.com", "https://example.com"},
		{"no url here", ""},
		{"http://not-https.com", ""},
		{"prefix https://a.com/path?q=1 suffix", "https://a.com/path?q=1"},
	}
	for _, tt := range tests {
		got := extractFirstHTTPS(tt.line)
		if got != tt.want {
			t.Errorf("extractFirstHTTPS(%q) = %q, want %q", tt.line, got, tt.want)
		}
	}
}

func TestCallbackPort(t *testing.T) {
	lp := &LoginProcess{URL: "https://example.com", callbackPort: 12345}
	if lp.CallbackPort() != 12345 {
		t.Errorf("CallbackPort() = %d, want 12345", lp.CallbackPort())
	}

	lp2 := &LoginProcess{URL: "https://example.com"}
	if lp2.CallbackPort() != 0 {
		t.Errorf("CallbackPort() = %d, want 0", lp2.CallbackPort())
	}
}

func TestWriteBrowserCaptureScript(t *testing.T) {
	dir := t.TempDir()
	scriptPath, urlFile, err := writeBrowserCaptureScript(dir)
	if err != nil {
		t.Fatalf("writeBrowserCaptureScript: %v", err)
	}

	// Script file exists and is executable.
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("stat script: %v", err)
	}
	if info.Mode().Perm()&0100 == 0 {
		t.Error("script is not executable")
	}

	// URL file path is inside the temp dir.
	if filepath.Dir(urlFile) != dir {
		t.Errorf("urlFile %q not in dir %q", urlFile, dir)
	}
}
