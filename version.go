package claudecli

import (
	"cmp"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// SDKVersion is the fallback version reported to the CLI via
// CLAUDE_AGENT_SDK_VERSION when this module's version cannot be read from the
// enclosing binary's module info — e.g. local dev builds, or when this repo is
// itself the main module. Consumers that import a tagged release advertise
// their pinned version automatically (see sdkVersion), so this placeholder is
// only ever seen for unversioned builds.
const SDKVersion = "0.0.0-dev"

// modulePath is this module's import path, used to find our own entry in the
// enclosing binary's module info.
const modulePath = "github.com/allbin/claudecli-go"

// sdkVersion resolves the value advertised to the CLI as
// CLAUDE_AGENT_SDK_VERSION. It reports the version this module was imported as,
// read from the enclosing binary's module info, and falls back to SDKVersion
// for unversioned (dev) builds. Because the entrypoint is already tagged
// "sdk-go", this honestly attributes traffic to this Go SDK at its real
// version rather than impersonating the upstream Agent SDK.
func sdkVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		// Imported as a dependency: our module appears in Deps.
		for _, d := range bi.Deps {
			if d.Path != modulePath {
				continue
			}
			if d.Replace != nil {
				d = d.Replace
			}
			if v := normalizeVersion(d.Version); v != "" {
				return v
			}
		}
		// This module is the main module (our own binaries): "(devel)" for
		// local builds, or a real tag when installed via `go install ...@vX`.
		if bi.Main.Path == modulePath {
			if v := normalizeVersion(bi.Main.Version); v != "" {
				return v
			}
		}
	}
	return SDKVersion
}

// normalizeVersion strips the leading "v" from a module version and treats the
// empty string and Go's "(devel)" placeholder as "no version".
func normalizeVersion(v string) string {
	if v == "" || v == "(devel)" {
		return ""
	}
	return strings.TrimPrefix(v, "v")
}

// MinCLIVersion is the minimum Claude CLI version required by this SDK.
const MinCLIVersion = "2.0.0"

var semverRe = regexp.MustCompile(`([0-9]+)\.([0-9]+)\.([0-9]+)`)

// VersionError indicates the CLI version is below minimum.
type VersionError struct {
	Found   string
	Minimum string
}

func (e *VersionError) Error() string {
	return fmt.Sprintf("claudecli: CLI version %s is below minimum %s", e.Found, e.Minimum)
}

// parseSemver extracts major.minor.patch from a version string.
// Returns (major, minor, patch, ok).
func parseSemver(s string) (int, int, int, bool) {
	m := semverRe.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, 0, false
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])
	return major, minor, patch, true
}

// compareSemver returns -1, 0, or 1 comparing a to b.
func compareSemver(a, b string) int {
	aMaj, aMin, aPat, _ := parseSemver(a)
	bMaj, bMin, bPat, _ := parseSemver(b)
	if c := cmp.Compare(aMaj, bMaj); c != 0 {
		return c
	}
	if c := cmp.Compare(aMin, bMin); c != 0 {
		return c
	}
	return cmp.Compare(aPat, bPat)
}

// CheckCLIVersion runs `claude -v` and returns an error if the version
// is below MinCLIVersion. Returns nil if the version is OK or cannot
// be determined (fail-open).
func CheckCLIVersion(ctx context.Context, binaryPath string) error {
	if binaryPath == "" {
		binaryPath = "claude"
	}
	resolved, err := exec.LookPath(binaryPath)
	if err != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, resolved, "-v").Output()
	if err != nil {
		return nil
	}

	maj, min, pat, ok := parseSemver(string(out))
	if !ok {
		return nil
	}

	found := fmt.Sprintf("%d.%d.%d", maj, min, pat)
	if compareSemver(found, MinCLIVersion) < 0 {
		return &VersionError{Found: found, Minimum: MinCLIVersion}
	}
	return nil
}
