//go:build windows

package file

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func replaceFile(source, target string) error {
	source16, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	target16, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(source16, target16, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func syncParentDir(string) error {
	// MoveFileEx with MOVEFILE_WRITE_THROUGH waits for the replacement to be
	// flushed. Windows does not expose a portable directory fsync operation.
	return nil
}

func secureFile(path string) error {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf("D:P(A;;FA;;;%s)(A;;FA;;;SY)(A;;FA;;;BA)", user.User.Sid.String()))
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
}
