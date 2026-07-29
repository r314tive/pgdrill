//go:build !darwin && !linux && !windows

package local

import (
	"fmt"
	"runtime"
)

func processIdentity(int) (string, error) {
	return "", fmt.Errorf("process identity inspection is unsupported on %s", runtime.GOOS)
}
