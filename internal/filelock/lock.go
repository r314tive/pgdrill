package filelock

import (
	"context"
	"time"
)

type Mode uint8

const (
	Shared Mode = iota
	Exclusive
)

func waitForRetry(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	timer := time.NewTimer(10 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
