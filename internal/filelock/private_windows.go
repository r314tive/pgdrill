//go:build windows

package filelock

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openPrivateFile(path string, flags int, _ os.FileMode) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	access := uint32(windows.GENERIC_READ)
	if flags&os.O_WRONLY != 0 {
		access = windows.GENERIC_WRITE
	}
	if flags&os.O_RDWR != 0 {
		access = windows.GENERIC_READ | windows.GENERIC_WRITE
	}
	disposition := uint32(windows.OPEN_EXISTING)
	if flags&os.O_CREATE != 0 {
		disposition = windows.OPEN_ALWAYS
		if flags&os.O_EXCL != 0 {
			disposition = windows.CREATE_NEW
		}
	}
	var securityAttributes *windows.SecurityAttributes
	var securityDescriptor *windows.SECURITY_DESCRIPTOR
	if flags&os.O_CREATE != 0 {
		securityDescriptor, err = privateWindowsSecurityDescriptor()
		if err != nil {
			return nil, err
		}
		securityAttributes = &windows.SecurityAttributes{
			Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
			SecurityDescriptor: securityDescriptor,
		}
	}
	handle, err := windows.CreateFile(
		name,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		securityAttributes,
		disposition,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	runtime.KeepAlive(securityDescriptor)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

func validatePrivatePathPermissions(string, os.FileInfo) error {
	return nil
}

func validatePrivateDescriptorPermissions(path string, file *os.File, _ os.FileInfo) error {
	actual, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("read private file security descriptor %s: %w", path, err)
	}
	expected, err := privateWindowsSecurityDescriptor()
	if err != nil {
		return err
	}
	if actual.String() != expected.String() {
		return fmt.Errorf("private file owner or DACL is not private: %s", path)
	}
	return nil
}

func privateWindowsSecurityDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read current Windows token user: %w", err)
	}
	sid := user.User.Sid.String()
	aces := []string{"(A;;FA;;;" + sid + ")"}
	if !strings.EqualFold(sid, "S-1-5-18") {
		aces = append(aces, "(A;;FA;;;SY)")
	}
	descriptor, err := windows.SecurityDescriptorFromString(
		"O:" + sid + "D:P" + strings.Join(aces, ""),
	)
	if err != nil {
		return nil, fmt.Errorf("build private Windows security descriptor: %w", err)
	}
	return descriptor, nil
}
