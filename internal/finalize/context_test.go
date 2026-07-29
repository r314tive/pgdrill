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

func TestContextUsesDefaultTimeoutWithNilParent(t *testing.T) {
	before := time.Now()
	ctx, cancel := Context(nil, 0)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("default finalization context has no deadline")
	}
	if minimum, maximum := before.Add(DefaultTimeout-time.Second), before.Add(DefaultTimeout+time.Second); deadline.Before(minimum) || deadline.After(maximum) {
		t.Fatalf("default deadline = %s, want between %s and %s", deadline, minimum, maximum)
	}
}
