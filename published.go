// This file makes network calls. Everything else in this package that reports
// on an installation — DetectInstall, and the AutoUpdateState it carries — is
// deliberately offline and cheap enough for a launch path. Keep the two apart:
// nothing here may be called from detection.

package claudecli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// nativeReleaseChannelURL is the CLI's own release-channel endpoint. GET
// <base>/<channel> returns the published version as plain text.
const nativeReleaseChannelURL = "https://downloads.claude.ai/claude-code-releases"

// npmRegistryURL is the public npm registry. The per-package dist-tags endpoint
// under it answers with a small JSON object of tag → version, which is what a
// channel lookup needs and a fraction of the full package document.
const npmRegistryURL = "https://registry.npmjs.org"

// homebrewCaskAPIURL serves one JSON document per cask, with the version
// Homebrew would install under .version.
const homebrewCaskAPIURL = "https://formulae.brew.sh/api/cask"

// defaultPublishedTimeout bounds a published-version lookup when the caller's
// context has no deadline. This is a background-tick call; it should give up
// long before a consumer notices.
const defaultPublishedTimeout = 10 * time.Second

// maxPublishedResponse caps how much of a lookup response is read. The
// endpoints answer with a version string or a small JSON document; anything
// larger is not the answer to this question.
const maxPublishedResponse = 1 << 20

// PublishedSource records which service answered a published-version lookup, so
// a caller can weigh the number and tell the user where it came from.
type PublishedSource string

const (
	// PublishedSourceNPMRegistry is the npm registry's dist-tags for
	// CLIPackageName. It answers for npm installs, global and CLI-local.
	PublishedSourceNPMRegistry PublishedSource = "npm-registry"

	// PublishedSourceReleaseChannel is the CLI's own release-channel endpoint,
	// which answers for native installs.
	PublishedSourceReleaseChannel PublishedSource = "release-channel"

	// PublishedSourceHomebrewCask is the Homebrew formulae API. Homebrew
	// installs track their cask, which can lag the other channels by hours to
	// days — so the cask, not the channel, is the honest comparison for them.
	PublishedSourceHomebrewCask PublishedSource = "homebrew-cask"

	// PublishedSourceNone means no trustworthy source exists for this install.
	PublishedSourceNone PublishedSource = "none"
)

// Published is the version published for an install's own release channel.
type Published struct {
	// Version is the published version (e.g. "2.1.241").
	Version string

	// Installed is what the CLI reports for itself right now, carried along so
	// one call answers the whole question. "" when the version probe failed.
	Installed string

	// UpdateAvailable is true only when both versions parsed and Installed is
	// older than Version. A version that cannot be parsed, on either side,
	// reports false rather than guessing.
	UpdateAvailable bool

	// Channel is the release channel consulted, or "" for a source that has no
	// channel of its own.
	Channel string

	// Source records which service answered.
	Source PublishedSource

	// URL is the endpoint that was queried, for a consumer that wants to show
	// or log where the number came from.
	URL string

	// Method is the detected install method the source was chosen from.
	Method InstallMethod

	// AutoUpdate is the install's background-updater state, carried from
	// detection. Never nil. An install that updates itself and last succeeded
	// two days ago does not need to be nagged about being one release behind.
	AutoUpdate *AutoUpdateState
}

// ErrPublishedUnknown matches the error returned by [LatestPublished] when no
// trustworthy published-version source exists for the detected install. Use
// errors.Is to distinguish it: an honest "cannot determine" is a correct
// answer, and materially better than a number from the wrong channel.
var ErrPublishedUnknown = errors.New("claudecli: no trustworthy published-version source for this install")

// PublishedUnknownError reports why a published-version lookup was not even
// attempted.
type PublishedUnknownError struct {
	// Method is the detected install method.
	Method InstallMethod

	// Reason is a human-readable explanation, ready to show a user.
	Reason string
}

func (e *PublishedUnknownError) Error() string {
	return fmt.Sprintf("claudecli: cannot determine the published version for a %s install: %s", e.Method, e.Reason)
}

func (e *PublishedUnknownError) Is(target error) bool { return target == ErrPublishedUnknown }

// PublishedOption configures a single [LatestPublished] call.
type PublishedOption func(*publishedOptions)

type publishedOptions struct {
	client  *http.Client
	channel string
	timeout time.Duration
}

// WithPublishedHTTPClient sets the HTTP client used for the lookup. Use it to
// route through a proxy, pin a transport, or stub the network in tests.
func WithPublishedHTTPClient(c *http.Client) PublishedOption {
	return func(o *publishedOptions) { o.client = c }
}

// WithPublishedChannel overrides the release channel the lookup asks about,
// instead of the one the install actually tracks. Useful for answering "what is
// on stable?" — but the answer is then no longer comparable to the installed
// version, which is the whole reason the channel is normally detected rather
// than chosen. Ignored by sources that have no channel of their own.
//
// The value must be a channel the CLI publishes ([ChannelLatest],
// [ChannelStable], [ChannelRC]); anything else is refused rather than
// interpolated into a URL.
func WithPublishedChannel(channel string) PublishedOption {
	return func(o *publishedOptions) { o.channel = channel }
}

// WithPublishedTimeout bounds the lookup. It applies only when the caller's
// context has no deadline of its own.
func WithPublishedTimeout(d time.Duration) PublishedOption {
	return func(o *publishedOptions) { o.timeout = d }
}

// LatestPublished reports the version published for the install's own release
// channel, using the default client's binary.
//
// # This one touches the network
//
// Unlike [DetectInstall], which is offline and cheap enough to run on a launch
// path, this makes an HTTP request. Put it on a slow background tick. It never
// runs as part of detection, and detection never runs it.
//
// # The channels do not agree
//
// This belongs in this library rather than in a consumer because only this
// library knows which channel a given install tracks, and the channels publish
// different numbers. Measured on one machine on one day: the npm registry's
// `latest` tag and the native `latest` channel both said 2.1.241, while native
// `stable` said 2.1.231, and the Homebrew cask lagged both. A consumer that
// compares an installed version against the wrong one manufactures a "behind"
// that is not true, and then nags about it.
//
// So the source is resolved from the detected install:
//
//   - npm-global and npm-local read the npm registry's dist-tags for
//     CLIPackageName — over plain HTTP, never by shelling out to `npm view`,
//     which assumes an npm on PATH that a server generally does not have.
//   - native reads the CLI's own release-channel endpoint, for the channel
//     recorded in [AutoUpdateState].
//   - Homebrew reads the cask it was installed from, because a brew install
//     tracks its cask rather than a channel: `claude-code` is stable and
//     `claude-code@latest` is latest, whatever the settings say.
//   - Everything else — a version manager, another OS package manager, an
//     install nothing could classify — has no trustworthy source, and returns
//     [ErrPublishedUnknown]. Borrowing npm's number for those would be a wrong
//     answer dressed as a right one.
//
// The lookup is also skipped, with [ErrPublishedUnknown], when
// CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC is set: the CLI's own updater
// suppresses exactly these fetches in that mode, and a library that owns the
// claude command must not restore the egress behind the user's back.
//
// A failed lookup is never fatal. The error is returned and the caller keeps
// whatever answer it had — a missed tick is not news.
func LatestPublished(ctx context.Context, opts ...PublishedOption) (*Published, error) {
	return defaultClient.LatestPublished(ctx, opts...)
}

// LatestPublished reports the version published for this client's install. See
// the package-level [LatestPublished] for the full contract.
func (c *Client) LatestPublished(ctx context.Context, opts ...PublishedOption) (*Published, error) {
	pub, err := latestPublished(ctx, c.binaryPath(), osInstallEnv(), opts)
	if err != nil {
		c.log().Debug("latest published", "err", err)
		return nil, err
	}
	c.log().Debug("latest published",
		"version", pub.Version, "installed", pub.Installed,
		"updateAvailable", pub.UpdateAvailable, "channel", pub.Channel,
		"source", pub.Source, "url", pub.URL, "method", pub.Method)
	return pub, nil
}

func latestPublished(ctx context.Context, binary string, env installEnv, opts []PublishedOption) (*Published, error) {
	var o publishedOptions
	for _, opt := range opts {
		opt(&o)
	}
	if o.channel != "" && !validChannel(o.channel) {
		return nil, fmt.Errorf("claudecli: %q is not a Claude CLI release channel", o.channel)
	}

	info, err := detectInstall(ctx, binary, env)
	if err != nil {
		return nil, err
	}

	if env.env("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC") != "" {
		return nil, &PublishedUnknownError{
			Method: info.Method,
			Reason: "network lookups are disabled by CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
		}
	}

	channel := o.channel
	if channel == "" {
		channel = info.AutoUpdate.Channel
	}

	pub := &Published{
		Installed:  info.Version,
		Channel:    channel,
		Method:     info.Method,
		AutoUpdate: info.AutoUpdate,
	}

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		timeout := o.timeout
		if timeout <= 0 {
			timeout = defaultPublishedTimeout
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	client := o.client
	if client == nil {
		client = http.DefaultClient
	}

	switch info.Method {
	case InstallNPMGlobal, InstallNPMLocal:
		pub.Source = PublishedSourceNPMRegistry
		pub.URL = npmDistTagsURL()
		pub.Version, err = fetchNPMDistTag(ctx, client, pub.URL, channel)

	case InstallNative:
		if !validChannel(channel) {
			return nil, &PublishedUnknownError{
				Method: info.Method,
				Reason: "the configured auto-update channel is not one the CLI publishes",
			}
		}
		pub.Source = PublishedSourceReleaseChannel
		pub.URL = nativeReleaseChannelURL + "/" + channel
		pub.Version, err = fetchReleaseChannel(ctx, client, pub.URL)

	case InstallPackageManager:
		cask, ok := homebrewCask(info)
		if !ok {
			return nil, &PublishedUnknownError{
				Method: info.Method,
				Reason: unknownPackageManagerReason(info),
			}
		}
		pub.Source = PublishedSourceHomebrewCask
		pub.URL = homebrewCaskAPIURL + "/" + cask + ".json"
		pub.Version, err = fetchHomebrewCask(ctx, client, pub.URL)

	default:
		pub.Source = PublishedSourceNone
		return nil, &PublishedUnknownError{
			Method: info.Method,
			Reason: "nothing records which release stream this install tracks",
		}
	}
	if err != nil {
		return nil, err
	}

	pub.UpdateAvailable = isBehind(pub.Installed, pub.Version)
	return pub, nil
}

// npmDistTagsURL builds the registry's dist-tags endpoint for the CLI package.
// The scoped package name is escaped rather than concatenated: the "@" and "/"
// are path-significant.
func npmDistTagsURL() string {
	return npmRegistryURL + "/-/package/" + url.PathEscape(CLIPackageName) + "/dist-tags"
}

// homebrewCask reports the cask a Homebrew install was installed from, and
// whether it is one this package recognizes.
//
// Only the two published cask names count. The name comes from a path segment,
// and an unrecognized one says nothing trustworthy about which stream the
// install tracks — matching a stable cask against the faster channel reads as
// "behind" when it is not, and the reverse reads as up to date when it is not.
func homebrewCask(info *InstallInfo) (string, bool) {
	if info.PackageManager != "homebrew" {
		return "", false
	}
	switch info.PackageName {
	case "claude-code", "claude-code@latest":
		return info.PackageName, true
	default:
		return "", false
	}
}

func unknownPackageManagerReason(info *InstallInfo) string {
	if info.PackageManager == "homebrew" {
		return fmt.Sprintf("cask %q is not one of the published Claude Code casks", info.PackageName)
	}
	return fmt.Sprintf("%s publishes no version feed this package can trust", info.PackageManager)
}

// fetchNPMDistTag reads one dist-tag from the registry. A channel with no
// matching tag is reported as such rather than falling back to another tag —
// the CLI's config accepts channel names the registry does not publish.
func fetchNPMDistTag(ctx context.Context, client *http.Client, endpoint, channel string) (string, error) {
	body, err := fetchPublished(ctx, client, endpoint)
	if err != nil {
		return "", err
	}
	var tags map[string]string
	if err := json.Unmarshal(body, &tags); err != nil {
		return "", fmt.Errorf("claudecli: decode npm dist-tags: %w", err)
	}
	version := tags[channel]
	if version == "" {
		return "", fmt.Errorf("claudecli: npm publishes no %q dist-tag for %s", channel, CLIPackageName)
	}
	return version, nil
}

// fetchReleaseChannel reads a native release channel, which answers with the
// version as plain text.
func fetchReleaseChannel(ctx context.Context, client *http.Client, endpoint string) (string, error) {
	body, err := fetchPublished(ctx, client, endpoint)
	if err != nil {
		return "", err
	}
	version := parseVersionOutput(strings.TrimSpace(string(body)))
	if version == "" {
		return "", fmt.Errorf("claudecli: release channel %s did not answer with a version", endpoint)
	}
	return version, nil
}

// fetchHomebrewCask reads the version Homebrew would install for a cask.
func fetchHomebrewCask(ctx context.Context, client *http.Client, endpoint string) (string, error) {
	body, err := fetchPublished(ctx, client, endpoint)
	if err != nil {
		return "", err
	}
	var cask struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &cask); err != nil {
		return "", fmt.Errorf("claudecli: decode homebrew cask: %w", err)
	}
	if cask.Version == "" {
		return "", fmt.Errorf("claudecli: homebrew cask %s reports no version", endpoint)
	}
	return cask.Version, nil
}

func fetchPublished(ctx context.Context, client *http.Client, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("claudecli: build request for %s: %w", endpoint, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("claudecli: fetch %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("claudecli: fetch %s: unexpected status %s", endpoint, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPublishedResponse))
	if err != nil {
		return nil, fmt.Errorf("claudecli: read %s: %w", endpoint, err)
	}
	return body, nil
}

// isBehind reports whether installed is older than published. Either version
// failing to parse reports false: an unparseable version is not evidence of
// being behind, and a false "update available" is the failure this whole call
// exists to avoid.
func isBehind(installed, published string) bool {
	if installed == "" || published == "" {
		return false
	}
	if _, _, _, ok := parseSemver(installed); !ok {
		return false
	}
	if _, _, _, ok := parseSemver(published); !ok {
		return false
	}
	return compareSemver(installed, published) < 0
}
