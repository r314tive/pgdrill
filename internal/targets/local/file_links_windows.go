//go:build windows

package local

import (
	"os"

	"golang.org/x/sys/windows"
)

func regularFileLinkCount(file *os.File) (uint64, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return 0, err
	}
	return uint64(info.NumberOfLinks), nil
}
