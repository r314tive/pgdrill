//go:build windows

package filelock

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestOpenPrivateRejectsPermissiveWindowsDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	file, err := OpenPrivate(path, os.O_CREATE|os.O_RDWR)
	if err != nil {
		t.Fatalf("OpenPrivate(create) error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPrivate(path, os.O_RDWR); err == nil {
		t.Fatal("OpenPrivate() accepted permissive Windows DACL")
	}
}
