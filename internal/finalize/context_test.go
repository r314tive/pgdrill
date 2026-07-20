package finalize

import (
	"context"
	"testing"
	"time"
)

type contextKey string

func TestContextDetachesCancellationAndPreservesValues(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.WithValue(context.Background(), contextKey("key"), "value"))
	cancelParent()

	ctx, cancel := Context(parent, time.Second)
	defer cancel()

	if err := ctx.Err(); err != nil {
		t.Fatalf("expected live finalization context, got %v", err)
	}
	if got := ctx.Value(contextKey("key")); got != "value" {
		t.Fatalf("unexpected context value %#v", got)
	}
}

func TestContextIsBounded(t *testing.T) {
	ctx, cancel := Context(context.Background(), 10*time.Millisecond)
	defer cancel()

	select {
	case <-ctx.Done():
		if ctx.Err() != context.DeadlineExceeded {
			t.Fatalf("expected deadline exceeded, got %v", ctx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("finalization context did not expire")
	}
}
