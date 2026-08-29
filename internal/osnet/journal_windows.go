//go:build windows

package osnet

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func replaceJournalFile(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func syncJournalDir(string) error { return nil }

func secureJournalFile(path string) error {
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
