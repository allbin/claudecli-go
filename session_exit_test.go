//go:build !windows

package claudecli

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func mustExitErr(t *testing.T, name string, args ...string) error {
	t.Helper()
	err := exec.Command(name, args...).Run()
	if err == nil {
		t.Fatalf("expected exit error from %s", name)
	}
	return err
}

func mustSignalErr(t *testing.T, sig syscall.Signal) error {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatalf("signal: %v", err)
	}
	err := cmd.Wait()
	if err == nil {
		t.Fatal("expected wait error after signal")
	}
	return err
}

func TestClassifyExit_Normal(t *testing.T) {
	ev := classifyExit(nil, nil, nil)
	if ev.Reason != ExitReasonNormal {
		t.Errorf("Reason = %s, want normal", ev.Reason)
	}
	if ev.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", ev.ExitCode)
	}
	if ev.Signal != "" {
		t.Errorf("Signal = %q, want empty", ev.Signal)
	}
	if ev.Err != nil {
		t.Errorf("Err = %v, want nil", ev.Err)
	}
	if ev.At.IsZero() {
		t.Error("At timestamp not set")
	}
}

func TestClassifyExit_ContextCanceled(t *testing.T) {
	ev := classifyExit(nil, context.Canceled, nil)
	if ev.Reason != ExitReasonContextCanceled {
		t.Errorf("Reason = %s, want context_canceled", ev.Reason)
	}
	if !errors.Is(ev.Err, context.Canceled) {
		t.Errorf("Err = %v, want context.Canceled", ev.Err)
	}
}

func TestClassifyExit_ContextCanceledOverridesSignal(t *testing.T) {
	waitErr := mustSignalErr(t, syscall.SIGTERM)
	ev := classifyExit(waitErr, context.Canceled, nil)
	// SDK-initiated kill: report context_canceled but still surface
	// the signal so consumers can see HOW the kill happened.
	if ev.Reason != ExitReasonContextCanceled {
		t.Errorf("Reason = %s, want context_canceled", ev.Reason)
	}
	if ev.Signal != "SIGTERM" {
		t.Errorf("Signal = %q, want SIGTERM", ev.Signal)
	}
}

func TestClassifyExit_Killed(t *testing.T) {
	waitErr := mustSignalErr(t, syscall.SIGKILL)
	ev := classifyExit(waitErr, nil, nil)
	if ev.Reason != ExitReasonKilled {
		t.Errorf("Reason = %s, want killed", ev.Reason)
	}
	if ev.Signal != "SIGKILL" {
		t.Errorf("Signal = %q, want SIGKILL", ev.Signal)
	}
	if ev.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1 for signaled exit", ev.ExitCode)
	}
}

func TestClassifyExit_Crashed(t *testing.T) {
	waitErr := mustExitErr(t, "false")
	ev := classifyExit(waitErr, nil, nil)
	if ev.Reason != ExitReasonCrashed {
		t.Errorf("Reason = %s, want crashed", ev.Reason)
	}
	if ev.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", ev.ExitCode)
	}
	if ev.Signal != "" {
		t.Errorf("Signal = %q, want empty", ev.Signal)
	}
}

func TestClassifyExit_PreservesUnderlyingError(t *testing.T) {
	cliErr := &Error{ExitCode: 1, Message: "boom"}
	ev := classifyExit(mustExitErr(t, "false"), nil, cliErr)
	if ev.Err != cliErr {
		t.Errorf("Err = %v, want passthrough cliErr", ev.Err)
	}
}

func TestClassifyExit_NonExitError(t *testing.T) {
	// Errors that aren't *exec.ExitError (start failure, IO error)
	// produce ExitCode=-1 and reason=unknown when no signal/context.
	ev := classifyExit(errors.New("some non-exit error"), nil, nil)
	if ev.Reason != ExitReasonUnknown {
		t.Errorf("Reason = %s, want unknown", ev.Reason)
	}
	if ev.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1", ev.ExitCode)
	}
}

func TestSessionEmitsCLIExitEvent_Normal(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)
		sim.sendResult()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	var lastEv Event
	var exit *CLIExitEvent
	deadline := time.After(3 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-session.Events():
			if !ok {
				break loop
			}
			lastEv = ev
			if e, ok := ev.(*CLIExitEvent); ok {
				exit = e
			}
		case <-deadline:
			t.Fatal("timeout draining events")
		}
	}

	if exit == nil {
		t.Fatal("missing CLIExitEvent")
	}
	if _, ok := lastEv.(*CLIExitEvent); !ok {
		t.Errorf("last event = %T, want *CLIExitEvent (must precede channel close)", lastEv)
	}
	if exit.Reason != ExitReasonNormal {
		t.Errorf("Reason = %s, want normal", exit.Reason)
	}
	if exit.At.IsZero() {
		t.Error("At timestamp not set")
	}
}
