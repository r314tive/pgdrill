//go:build !linux && !windows

package local

import (
	"fmt"
	"runtime"
)

func openIdentityBoundProcess(int, string) (recoveredProcessHandle, error) {
	return nil, fmt.Errorf(
		"identity-bound recovered process signalling is unsupported on %s",
		runtime.GOOS,
	)
}
