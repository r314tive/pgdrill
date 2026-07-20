package finalize

import (
	"context"
	"time"
)

const DefaultTimeout = 5 * time.Minute

// Context preserves parent values while allowing bounded cleanup and evidence
// writes to complete after the operation context has been canceled.
func Context(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return context.WithTimeout(context.WithoutCancel(parent), timeout)
}
