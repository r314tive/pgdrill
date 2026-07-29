//go:build !unix && !windows

package local

import (
	"fmt"
	"os"
	"runtime"
)

func regularFileLinkCount(*os.File) (uint64, error) {
	return 0, fmt.Errorf("file link-count inspection is unsupported on %s", runtime.GOOS)
}
