package claudecli_test

import (
	"context"
	"fmt"

	"github.com/allbin/claudecli-go"
)

func ExampleNewWithExecutor_fixture() {
	exec, err := claudecli.NewFixtureExecutorFromFile("testdata/basic.jsonl")
	if err != nil {
		panic(err)
	}
	client := claudecli.NewWithExecutor(exec)

	text, result, err := client.RunText(context.Background(), "ignored prompt")
	if err != nil {
		panic(err)
	}
	fmt.Printf("Got %d chars, cost $%.4f\n", len(text), result.CostUSD)
	// Output: Got 28 chars, cost $0.0124
}

func ExampleStream_events() {
	exec, err := claudecli.NewFixtureExecutorFromFile("testdata/basic.jsonl")
	if err != nil {
		panic(err)
	}
	client := claudecli.NewWithExecutor(exec)

	stream := client.Run(context.Background(), "ignored prompt")

	var eventCount int
	for event := range stream.Events() {
		_ = event
		eventCount++
	}
	fmt.Printf("Received %d events, state: %s\n", eventCount, stream.State())
	// Output: Received 6 events, state: done
}
