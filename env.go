package claudecli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrConfigDirNotAbsolute is returned by [Client.ProjectsDir] when the
// CLAUDE_CONFIG_DIR a client's CLI sees is set but empty or relative. The CLI
// uses such a value as given, so its projects root is relative to each run's
// working directory and has no single answer.
var ErrConfigDirNotAbsolute = errors.New("claudecli: CLAUDE_CONFIG_DIR is not an absolute path")

// cliEnv is the environment a CLI spawned by a client sees: the client's
// default WithEnv entries layered over the process environment, the way
// buildEnv merges them.
type cliEnv map[string]string

// cliEnv returns the WithEnv entries from the client's default options. A
// per-call WithEnv passed to Run or Connect is not part of it.
func (c *Client) cliEnv() cliEnv {
	return resolveOptions(c.defaults, nil).env
}

// lookup reports a variable as the spawned CLI sees it. A WithEnv entry wins
// even when its value is empty, because buildEnv replaces the process value
// rather than falling back to it.
func (e cliEnv) lookup(key string) (string, bool) {
	if v, ok := e[key]; ok {
		return v, true
	}
	return os.LookupEnv(key)
}

func (e cliEnv) getenv(key string) string {
	v, _ := e.lookup(key)
	return v
}

// userHomeDir resolves the home directory from HOME (USERPROFILE on Windows),
// preferring a non-empty WithEnv entry over the process's own.
func (e cliEnv) userHomeDir() (string, error) {
	key := "HOME"
	if runtime.GOOS == "windows" {
		key = "USERPROFILE"
	}
	if v := e[key]; v != "" {
		return v, nil
	}
	return os.UserHomeDir()
}

// cmdEnv is the Env for a helper subprocess (version probe, updater, auth).
// It is nil, inheriting the process environment unchanged, when the client
// sets no WithEnv entries.
func (e cliEnv) cmdEnv() []string {
	if len(e) == 0 {
		return nil
	}
	env := make([]string, 0, len(os.Environ())+len(e))
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if _, ok := e[key]; ok {
			continue
		}
		env = append(env, kv)
	}
	for k, v := range e {
		env = append(env, k+"="+v)
	}
	return env
}

// ProjectsDir reports the projects root the default client's CLI writes
// session transcripts under. See [Client.ProjectsDir].
func ProjectsDir() (string, error) {
	return defaultClient.ProjectsDir()
}

// ProjectsDir reports the projects root this client's CLI writes session and
// workflow transcripts under: $CLAUDE_CONFIG_DIR/projects, else
// ~/.claude/projects. Use it to check that a path taken from the stream, such
// as [WorkflowLaunch.TranscriptDir], is inside that root.
//
// CLAUDE_CONFIG_DIR is resolved the way the spawned CLI sees it: a WithEnv
// entry in the client's default options overrides the process environment.
// Without this, a client built with WithEnv(map[string]string{
// "CLAUDE_CONFIG_DIR": dir}) looks for transcripts under the process's config
// dir while its CLI writes them under dir. A WithEnv passed per call to Run or
// Connect is not seen.
//
// A set but empty CLAUDE_CONFIG_DIR does not fall back to ~/.claude. The
// spawned CLI receives the empty value and, on CLI 2.1.270, writes projects/
// relative to its working directory. That value, and any other relative one,
// returns [ErrConfigDirNotAbsolute].
//
// A CLAUDE_CONFIG_DIR set in a settings file's env block (managed settings,
// or WithSettings) also relocates the CLI's config home and is not seen here.
func (c *Client) ProjectsDir() (string, error) {
	return c.cliEnv().projectsDir()
}

func (e cliEnv) projectsDir() (string, error) {
	if d, ok := e.lookup("CLAUDE_CONFIG_DIR"); ok {
		if !filepath.IsAbs(d) {
			return "", fmt.Errorf("%w: %q", ErrConfigDirNotAbsolute, d)
		}
		return filepath.Join(d, "projects"), nil
	}
	home, err := e.userHomeDir()
	if err != nil {
		return "", fmt.Errorf("claudecli: resolve projects dir: %w", err)
	}
	return filepath.Join(home, ".claude", "projects"), nil
}
