package claudecli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// stubTransport answers from a fixed URL→response table and records what was
// asked, so no test in this file can reach the network.
type stubTransport struct {
	responses map[string]stubResponse
	requested []string
}

type stubResponse struct {
	status int
	body   string
}

func (s *stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.requested = append(s.requested, req.URL.String())
	resp, ok := s.responses[req.URL.String()]
	if !ok {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Status:     "404 Not Found",
			Body:       io.NopCloser(strings.NewReader("no such key")),
			Request:    req,
		}, nil
	}
	return &http.Response{
		StatusCode: resp.status,
		Status:     strconv.Itoa(resp.status) + " " + http.StatusText(resp.status),
		Body:       io.NopCloser(strings.NewReader(resp.body)),
		Request:    req,
	}, nil
}

// refusingTransport fails loudly if a lookup that should never leave the
// process tries to.
type refusingTransport struct{ t *testing.T }

func (r refusingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.t.Errorf("unexpected network request to %s", req.URL)
	return nil, errors.New("network access is not allowed in tests")
}

// Versions measured from the live sources on one day. They disagree, which is
// the entire reason the channel has to be detected rather than assumed.
const (
	publishedLatest = "2.1.241"
	publishedStable = "2.1.231"
	caskStable      = "2.1.231"
	caskLatest      = "2.1.241"
)

func publishedStubs() map[string]stubResponse {
	return map[string]stubResponse{
		npmDistTagsURL():                                {http.StatusOK, `{"stable":"` + publishedStable + `","latest":"` + publishedLatest + `","next":"` + publishedLatest + `"}`},
		nativeReleaseChannelURL + "/latest":             {http.StatusOK, publishedLatest + "\n"},
		nativeReleaseChannelURL + "/stable":             {http.StatusOK, publishedStable + "\n"},
		homebrewCaskAPIURL + "/claude-code.json":        {http.StatusOK, `{"token":"claude-code","version":"` + caskStable + `"}`},
		homebrewCaskAPIURL + "/claude-code@latest.json": {http.StatusOK, `{"token":"claude-code@latest","version":"` + caskLatest + `"}`},
	}
}

// settingsWithChannel is the CLI's user settings file naming a channel.
func settingsWithChannel(channel string) string {
	return `{"theme":"dark","autoUpdatesChannel":"` + channel + `"}`
}

func TestLatestPublishedChannelSelection(t *testing.T) {
	tests := []struct {
		name        string
		realPath    string
		files       map[string]string
		opts        []PublishedOption
		wantVersion string
		wantChannel string
		wantSource  PublishedSource
		wantURL     string
	}{
		{
			name:        "npm global reads the registry's latest dist-tag",
			realPath:    "/usr/local/lib/node_modules/@anthropic-ai/claude-code/cli.js",
			wantVersion: publishedLatest,
			wantChannel: ChannelLatest,
			wantSource:  PublishedSourceNPMRegistry,
			wantURL:     npmDistTagsURL(),
		},
		{
			name:     "npm global on the stable channel reads the stable dist-tag",
			realPath: "/usr/local/lib/node_modules/@anthropic-ai/claude-code/cli.js",
			files: map[string]string{
				"/home/u/.claude/settings.json": settingsWithChannel(ChannelStable),
			},
			wantVersion: publishedStable,
			wantChannel: ChannelStable,
			wantSource:  PublishedSourceNPMRegistry,
			wantURL:     npmDistTagsURL(),
		},
		{
			name:        "npm local reads the registry too",
			realPath:    "/home/u/.claude/local/claude",
			wantVersion: publishedLatest,
			wantChannel: ChannelLatest,
			wantSource:  PublishedSourceNPMRegistry,
			wantURL:     npmDistTagsURL(),
		},
		{
			name:        "native defaults to the latest release channel",
			realPath:    "/home/u/.local/share/claude/versions/2.1.239",
			files:       map[string]string{"/home/u/.local/share/claude/versions/2.1.239": elfHeader},
			wantVersion: publishedLatest,
			wantChannel: ChannelLatest,
			wantSource:  PublishedSourceReleaseChannel,
			wantURL:     nativeReleaseChannelURL + "/latest",
		},
		{
			name:     "native on the stable channel reads a different number",
			realPath: "/home/u/.local/share/claude/versions/2.1.239",
			files: map[string]string{
				"/home/u/.local/share/claude/versions/2.1.239": elfHeader,
				"/home/u/.claude/settings.json":                settingsWithChannel(ChannelStable),
			},
			wantVersion: publishedStable,
			wantChannel: ChannelStable,
			wantSource:  PublishedSourceReleaseChannel,
			wantURL:     nativeReleaseChannelURL + "/stable",
		},
		{
			name:        "an explicit channel overrides the detected one",
			realPath:    "/home/u/.local/share/claude/versions/2.1.239",
			files:       map[string]string{"/home/u/.local/share/claude/versions/2.1.239": elfHeader},
			opts:        []PublishedOption{WithPublishedChannel(ChannelStable)},
			wantVersion: publishedStable,
			wantChannel: ChannelStable,
			wantSource:  PublishedSourceReleaseChannel,
			wantURL:     nativeReleaseChannelURL + "/stable",
		},
		{
			name:        "the stable homebrew cask tracks its own cask, not the settings channel",
			realPath:    "/opt/homebrew/Caskroom/claude-code/2.1.231/claude",
			files:       map[string]string{"/home/u/.claude/settings.json": settingsWithChannel(ChannelLatest)},
			wantVersion: caskStable,
			wantChannel: ChannelStable,
			wantSource:  PublishedSourceHomebrewCask,
			wantURL:     homebrewCaskAPIURL + "/claude-code.json",
		},
		{
			name:        "the latest homebrew cask tracks latest",
			realPath:    "/opt/homebrew/Caskroom/claude-code@latest/2.1.241/claude",
			wantVersion: caskLatest,
			wantChannel: ChannelLatest,
			wantSource:  PublishedSourceHomebrewCask,
			wantURL:     homebrewCaskAPIURL + "/claude-code@latest.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &stubTransport{responses: publishedStubs()}
			env := fakeInstallEnv(tt.files)
			env.lookPath = func(string) (string, error) { return tt.realPath, nil }

			opts := append([]PublishedOption{WithPublishedHTTPClient(&http.Client{Transport: stub})}, tt.opts...)
			pub, err := latestPublished(context.Background(), "claude", env, opts)
			if err != nil {
				t.Fatalf("latestPublished: %v", err)
			}
			if pub.Version != tt.wantVersion {
				t.Errorf("Version = %q, want %q", pub.Version, tt.wantVersion)
			}
			if pub.Channel != tt.wantChannel {
				t.Errorf("Channel = %q, want %q", pub.Channel, tt.wantChannel)
			}
			if pub.Source != tt.wantSource {
				t.Errorf("Source = %q, want %q", pub.Source, tt.wantSource)
			}
			if pub.URL != tt.wantURL {
				t.Errorf("URL = %q, want %q", pub.URL, tt.wantURL)
			}
			if len(stub.requested) != 1 || stub.requested[0] != tt.wantURL {
				t.Errorf("requested %v, want exactly [%s]", stub.requested, tt.wantURL)
			}
		})
	}
}

// Borrowing another channel's number would be a wrong answer dressed as a right
// one, so these report that they cannot tell.
func TestLatestPublishedRefusesToGuess(t *testing.T) {
	tests := []struct {
		name       string
		realPath   string
		files      map[string]string
		wantMethod InstallMethod
		wantReason string
	}{
		{
			name:       "version manager",
			realPath:   "/home/u/.asdf/shims/claude",
			wantMethod: InstallVersionManager,
			wantReason: "release stream",
		},
		{
			name:       "winget",
			realPath:   `C:\Users\u\AppData\Local\Microsoft\WinGet\Packages\Anthropic.ClaudeCode\claude.exe`,
			wantMethod: InstallPackageManager,
			wantReason: "winget",
		},
		{
			name:       "mise",
			realPath:   "/home/u/.mise/installs/claude/2.1.87/bin/claude",
			wantMethod: InstallPackageManager,
			wantReason: "mise",
		},
		{
			name:       "an unrecognized homebrew cask",
			realPath:   "/opt/homebrew/Caskroom/claude-code-beta/2.1.87/claude",
			wantMethod: InstallPackageManager,
			wantReason: "claude-code-beta",
		},
		{
			name:       "unknown",
			realPath:   "/opt/mystery/claude",
			wantMethod: InstallUnknown,
			wantReason: "release stream",
		},
		{
			name:     "a native install on a channel the CLI does not publish",
			realPath: "/home/u/.local/share/claude/versions/2.1.239",
			files: map[string]string{
				"/home/u/.local/share/claude/versions/2.1.239": elfHeader,
				"/home/u/.claude/settings.json":                settingsWithChannel("nightly"),
			},
			wantMethod: InstallNative,
			wantReason: "channel",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := fakeInstallEnv(tt.files)
			env.lookPath = func(string) (string, error) { return tt.realPath, nil }

			opts := []PublishedOption{WithPublishedHTTPClient(&http.Client{Transport: refusingTransport{t}})}
			_, err := latestPublished(context.Background(), "claude", env, opts)
			if !errors.Is(err, ErrPublishedUnknown) {
				t.Fatalf("err = %v, want ErrPublishedUnknown", err)
			}
			var unknown *PublishedUnknownError
			if !errors.As(err, &unknown) {
				t.Fatalf("errors.As(%v, *PublishedUnknownError) = false", err)
			}
			if unknown.Method != tt.wantMethod {
				t.Errorf("Method = %q, want %q", unknown.Method, tt.wantMethod)
			}
			if !strings.Contains(unknown.Reason, tt.wantReason) {
				t.Errorf("Reason = %q, want it to mention %q", unknown.Reason, tt.wantReason)
			}
		})
	}
}

// The CLI's own updater suppresses these fetches in essential-traffic mode; a
// library that owns the claude command must not restore the egress.
func TestLatestPublishedHonoursEssentialTrafficMode(t *testing.T) {
	env := fakeInstallEnv(nil)
	env.lookPath = func(string) (string, error) { return "/home/u/.claude/local/claude", nil }
	env.getenv = func(name string) string {
		if name == "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC" {
			return "1"
		}
		return ""
	}

	opts := []PublishedOption{WithPublishedHTTPClient(&http.Client{Transport: refusingTransport{t}})}
	_, err := latestPublished(context.Background(), "claude", env, opts)
	if !errors.Is(err, ErrPublishedUnknown) {
		t.Fatalf("err = %v, want ErrPublishedUnknown", err)
	}
	if !strings.Contains(err.Error(), "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC") {
		t.Errorf("Error() = %q, want it to name the variable", err)
	}
}

func TestLatestPublishedUpdateAvailable(t *testing.T) {
	tests := []struct {
		name      string
		installed string
		want      bool
	}{
		{name: "behind", installed: "2.1.239", want: true},
		{name: "current", installed: publishedLatest, want: false},
		{name: "ahead of the channel", installed: "2.2.0", want: false},
		{name: "unparseable", installed: "dev", want: false},
		{name: "unknown", installed: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := fakeInstallEnv(nil)
			env.lookPath = func(string) (string, error) { return "/home/u/.claude/local/claude", nil }
			env.runVersion = func(context.Context, string) (string, error) {
				if tt.installed == "" {
					return "", errors.New("probe failed")
				}
				return tt.installed, nil
			}

			stub := &stubTransport{responses: publishedStubs()}
			pub, err := latestPublished(context.Background(), "claude", env, []PublishedOption{WithPublishedHTTPClient(&http.Client{Transport: stub})})
			if err != nil {
				t.Fatalf("latestPublished: %v", err)
			}
			if pub.Installed != tt.installed {
				t.Errorf("Installed = %q, want %q", pub.Installed, tt.installed)
			}
			if pub.UpdateAvailable != tt.want {
				t.Errorf("UpdateAvailable = %v, want %v (installed %q vs published %q)", pub.UpdateAvailable, tt.want, tt.installed, pub.Version)
			}
		})
	}
}

// The rc channel is settable in the CLI's own config but nothing is published
// under it. Report the failure rather than quietly answering with latest.
func TestLatestPublishedReportsAnUnpublishedChannel(t *testing.T) {
	env := fakeInstallEnv(map[string]string{
		"/home/u/.local/share/claude/versions/2.1.239": elfHeader,
		"/home/u/.claude/settings.json":                settingsWithChannel(ChannelRC),
	})
	env.lookPath = func(string) (string, error) { return "/home/u/.local/share/claude/versions/2.1.239", nil }

	stub := &stubTransport{responses: publishedStubs()} // no /rc entry: 404, as measured
	_, err := latestPublished(context.Background(), "claude", env, []PublishedOption{WithPublishedHTTPClient(&http.Client{Transport: stub})})
	if err == nil {
		t.Fatal("err = nil, want a lookup failure for an unpublished channel")
	}
	if !strings.Contains(err.Error(), "/rc") {
		t.Errorf("Error() = %q, want it to name the channel that failed", err)
	}
	if strings.Contains(err.Error(), publishedLatest) {
		t.Errorf("Error() = %q, want no fallback to another channel's number", err)
	}
}

func TestLatestPublishedRejectsAnUnknownChannelOption(t *testing.T) {
	env := fakeInstallEnv(nil)
	env.lookPath = func(string) (string, error) { return "/home/u/.claude/local/claude", nil }

	_, err := latestPublished(context.Background(), "claude", env, []PublishedOption{
		WithPublishedHTTPClient(&http.Client{Transport: refusingTransport{t}}),
		WithPublishedChannel("../../etc/passwd"),
	})
	if err == nil {
		t.Fatal("err = nil, want an unvalidated channel to be refused before it reaches a URL")
	}
	if errors.Is(err, ErrPublishedUnknown) {
		t.Errorf("err = %v, want a validation error rather than ErrPublishedUnknown", err)
	}
}

func TestLatestPublishedMissingCLI(t *testing.T) {
	env := fakeInstallEnv(nil)
	env.lookPath = func(string) (string, error) { return "", errors.New("not found") }

	_, err := latestPublished(context.Background(), "claude", env, []PublishedOption{
		WithPublishedHTTPClient(&http.Client{Transport: refusingTransport{t}}),
	})
	if !errors.Is(err, ErrCLINotFound) {
		t.Fatalf("err = %v, want ErrCLINotFound", err)
	}
}

func TestLatestPublishedSurfacesNonOKStatus(t *testing.T) {
	env := fakeInstallEnv(nil)
	env.lookPath = func(string) (string, error) { return "/home/u/.claude/local/claude", nil }

	stub := &stubTransport{responses: map[string]stubResponse{
		npmDistTagsURL(): {http.StatusServiceUnavailable, "upstream down"},
	}}
	_, err := latestPublished(context.Background(), "claude", env, []PublishedOption{WithPublishedHTTPClient(&http.Client{Transport: stub})})
	if err == nil {
		t.Fatal("err = nil, want the HTTP status surfaced")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("Error() = %q, want it to carry the status", err)
	}
}

func TestNPMDistTagsURLEscapesTheScopedPackage(t *testing.T) {
	got := npmDistTagsURL()
	want := "https://registry.npmjs.org/-/package/@anthropic-ai%2Fclaude-code/dist-tags"
	if got != want {
		t.Errorf("npmDistTagsURL() = %q, want %q", got, want)
	}
}
