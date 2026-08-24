package claudecli

import (
	"os"
	"path/filepath"
)

// InstallPathEntry is one copy of the CLI found on PATH.
//
// Entries are reported in PATH order, which is the order the shell resolves
// them in, so the first Active entry is the one that runs. Two copies is a
// normal state, not an error: it never blocks detection, an update, or a
// published-version lookup.
type InstallPathEntry struct {
	// Path is the binary as found on PATH, before symlinks are resolved.
	Path string

	// RealPath is Path with all symlinks resolved. Distinct copies are
	// distinguished by this, not by Path — two PATH directories that are
	// symlinks to each other are one copy, reported once.
	RealPath string

	// Method is how this copy was installed, classified the same way
	// InstallInfo.Method is. No version probe is run for it: that would be a
	// process spawn per copy on a path that has to stay cheap.
	Method InstallMethod

	// Active is true for the copy that actually runs — the one the rest of
	// InstallInfo describes. Exactly one entry has it set.
	Active bool
}

// Shadowed returns the copies that are not the one running.
//
// A non-empty result is what a "another copy of claude is installed at X"
// warning is made of. It is a warning and never a refusal: the shadowed copy
// does nothing until PATH changes — and then it does everything.
func (i *InstallInfo) Shadowed() []InstallPathEntry {
	var out []InstallPathEntry
	for _, e := range i.PathEntries {
		if !e.Active {
			out = append(out, e)
		}
	}
	return out
}

// osPathDirs reports the PATH directories in order, matching exec.LookPath's
// own reading of PATH — including its treatment of an empty entry as the
// working directory.
func osPathDirs() []string {
	dirs := filepath.SplitList(os.Getenv("PATH"))
	for i, dir := range dirs {
		if dir == "" {
			dirs[i] = "."
		}
	}
	return dirs
}

// findPathEntries walks every PATH directory looking for further copies of the
// CLI, instead of stopping at the first hit the way exec.LookPath does.
//
// # Why a second copy is worth the walk
//
// InstallInfo.ConfigMismatch catches a shadowing install only when the two
// copies were installed by *different* methods and the config file records the
// loser. It says nothing when both agree — and the common drift is exactly
// that: a native install and an old npm-global one, config says native,
// detection says native, and they agree while fifteen months of version skew
// sits one PATH change away. Measured on one machine: a native 2.1.241 in
// ~/.local/bin winning over an npm-global 2.0.14 in /usr/local/bin, with
// ConfigMismatch correctly false throughout.
//
// The walk is the same work exec.LookPath already does, minus the early exit,
// plus a classification per copy — file reads, no process spawn. Measured at
// ~100µs on a two-copy machine, against the ~200ms `claude -v` probe detection
// already pays, so it is free at the resolution anyone cares about. A version
// per copy would not be: that is a spawn each, and this has to stay cheap
// enough for a launch path.
//
// active and activeReal are the winning binary as detection already resolved
// it. It is always present in the result, marked Active, even when it is an
// explicit path PATH does not contain: what runs is what runs.
func findPathEntries(binary, active, activeReal string, env installEnv) []InstallPathEntry {
	var (
		entries []InstallPathEntry
		seen    = map[string]bool{}
	)

	entryFor := func(found string) (InstallPathEntry, bool) {
		real := found
		if resolved, err := env.evalSymlink(found); err == nil && resolved != "" {
			real = resolved
		}
		// Distinct copies, not distinct spellings: a duplicated PATH entry, or
		// two directories symlinked together, is one copy. Matching the winner
		// on the resolved path rather than the found one means a copy reached
		// under a second name is still recognized as the one that runs.
		if seen[real] {
			return InstallPathEntry{}, false
		}
		seen[real] = true
		return InstallPathEntry{
			Path:     found,
			RealPath: real,
			Method:   classifyInstall(real, env).method,
			Active:   real == activeReal,
		}, true
	}

	if env.pathDirs != nil {
		name := filepath.Base(binary)
		for _, dir := range env.pathDirs() {
			found, err := env.lookPath(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			if entry, ok := entryFor(found); ok {
				entries = append(entries, entry)
			}
		}
	}

	if !hasActive(entries) {
		// The winner is not reachable on PATH under that name — an explicitly
		// configured binary, or a PATH that changed under us. It leads the
		// list, because it is still what runs.
		if entry, ok := entryFor(active); ok {
			entry.Active = true
			entries = append([]InstallPathEntry{entry}, entries...)
		}
	}
	return entries
}

func hasActive(entries []InstallPathEntry) bool {
	for _, e := range entries {
		if e.Active {
			return true
		}
	}
	return false
}
