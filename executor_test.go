package claudecli

import (
	"context"
	"testing"
)

func TestLocalExecutor_AutoVersionCheck(t *testing.T) {
	// Can't test with real binary easily, but we can verify the sync.Once
	// caching behavior by calling Start twice on an executor with a
	// nonexistent binary — the version check should fail-open (nil)
	// and start should fail at LookPath.
	e := &LocalExecutor{BinaryPath: "/nonexistent/claude-test-binary"}

	_, err := e.Start(context.Background(), &StartConfig{})
	if err == nil {
		t.Fatal("expected error for nonexistent binary")
	}

	// Version check ran (fail-open), so versionErr should be nil
	if e.versionErr != nil {
		t.Errorf("expected nil versionErr for unresolvable binary, got %v", e.versionErr)
	}
}

func TestLocalExecutor_SkipVersionCheck(t *testing.T) {
	e := &LocalExecutor{BinaryPath: "/nonexistent/claude-test-binary"}

	_, _ = e.Start(context.Background(), &StartConfig{SkipVersionCheck: true})

	// versionOnce should NOT have been called — versionErr stays nil
	if e.versionErr != nil {
		t.Errorf("expected nil versionErr when skipping, got %v", e.versionErr)
	}
}

func TestLocalExecutor_VersionCheckCached(t *testing.T) {
	// Simulate version error by setting versionErr directly, then verify
	// subsequent Start calls return it without re-running the check.
	e := &LocalExecutor{BinaryPath: "claude"}
	e.versionErr = &VersionError{Found: "1.0.0", Minimum: MinCLIVersion}
	e.versionOnce.Do(func() {}) // mark as done

	_, err := e.Start(context.Background(), &StartConfig{})
	if err == nil {
		t.Fatal("expected version error")
	}
	verr, ok := err.(*VersionError)
	if !ok {
		t.Fatalf("expected *VersionError, got %T: %v", err, err)
	}
	if verr.Found != "1.0.0" {
		t.Errorf("found = %q", verr.Found)
	}
}

func TestLocalExecutor_VersionCheckSkipBypass(t *testing.T) {
	// Even with a cached version error, SkipVersionCheck should bypass it.
	e := &LocalExecutor{BinaryPath: "/nonexistent/claude-test-binary"}
	e.versionErr = &VersionError{Found: "1.0.0", Minimum: MinCLIVersion}
	e.versionOnce.Do(func() {})

	_, err := e.Start(context.Background(), &StartConfig{SkipVersionCheck: true})
	// Should fail at LookPath, not version check
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := err.(*VersionError); ok {
		t.Error("expected non-VersionError when skipping, got VersionError")
	}
}
