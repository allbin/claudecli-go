package claudecli

import (
	"context"
	"testing"
)

// The receipt tells a caller which queued commands survived — data the CLI
// already returned and the SDK used to discard.
func TestInterruptWithQueuedReceipt(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	got := make(chan map[string]any, 1)
	go func() {
		sim.handleInitAndReady(t)
		msg := sim.respondSuccessWithBody(t, `{"still_queued":[],"cancelled":["uuid-a","uuid-b"]}`)
		got <- msg["request"].(map[string]any)
		sim.sendResult()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	receipt, err := session.InterruptWithQueued(true)
	if err != nil {
		t.Fatal(err)
	}

	request := <-got
	if request["subtype"] != "interrupt" {
		t.Fatalf("subtype = %v", request["subtype"])
	}
	if request["cancel_queued"] != true {
		t.Errorf("cancel_queued = %v, want true", request["cancel_queued"])
	}
	if len(receipt.Cancelled) != 2 || receipt.Cancelled[0] != "uuid-a" {
		t.Errorf("Cancelled = %v", receipt.Cancelled)
	}
	if len(receipt.StillQueued) != 0 {
		t.Errorf("StillQueued = %v, want empty when cancelling", receipt.StillQueued)
	}

	if _, err := session.Wait(); err != nil {
		t.Fatal(err)
	}
}

// Plain Interrupt must not send cancel_queued at all — an older CLI ignores
// unknown fields, but sending it would change semantics on a new one.
func TestInterruptOmitsCancelQueued(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	got := make(chan map[string]any, 1)
	go func() {
		sim.handleInitAndReady(t)
		msg := sim.respondSuccess(t)
		got <- msg["request"].(map[string]any)
		sim.sendResult()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	if err := session.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if _, present := (<-got)["cancel_queued"]; present {
		t.Error("plain Interrupt sent cancel_queued")
	}

	if _, err := session.Wait(); err != nil {
		t.Fatal(err)
	}
}

// A CLI predating the receipt answers with an empty body; that is not an error.
func TestInterruptEmptyReceiptIsNotAnError(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)
		sim.respondSuccess(t)
		sim.sendResult()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	receipt, err := session.InterruptWithQueued(false)
	if err != nil {
		t.Fatalf("empty receipt treated as error: %v", err)
	}
	if receipt == nil {
		t.Fatal("nil receipt")
	}

	if _, err := session.Wait(); err != nil {
		t.Fatal(err)
	}
}
