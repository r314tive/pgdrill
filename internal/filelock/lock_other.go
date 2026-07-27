//go:build !unix && !windows

package filelock

import (
	"context"
	"fmt"
	"os"
	"runtime"
)

func Lock(context.Context, *os.File, Mode) error {
	return fmt.Errorf("advisory file locking is unsupported on %s", runtime.GOOS)
}

func Unlock(*os.File) error {
	return nil
}
