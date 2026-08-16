package llm

import (
	"context"
	"testing"
)

func TestContextUsageRecorderReceivesStreamContext(t *testing.T) {
	type markerKey struct{}
	ctx := context.WithValue(context.Background(), markerKey{}, "parent")
	var got context.Context
	SetUsageRecorderContext(func(ctx context.Context, _ AssistantMessage) { got = ctx })
	t.Cleanup(func() { SetUsageRecorderContext(nil) })

	recordUsage(ctx, AssistantMessage{Usage: Usage{Input: 1}})
	if got == nil || got.Value(markerKey{}) != "parent" {
		t.Fatalf("usage recorder lost stream context: %v", got)
	}
}

func TestZeroUsageDoesNotCallContextRecorder(t *testing.T) {
	called := false
	SetUsageRecorderContext(func(context.Context, AssistantMessage) { called = true })
	t.Cleanup(func() { SetUsageRecorderContext(nil) })
	recordUsage(context.Background(), AssistantMessage{})
	if called {
		t.Fatal("zero usage should not invoke recorder")
	}
}
