//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func ensureElevated() error {
	if os.Getenv("FLEXCONNECTD_NO_ELEVATE") == "1" {
		return nil
	}
	admin, err := isAdmin()
	if err != nil {
		return err
	}
	if admin {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	params := quoteArgs(os.Args[1:])
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	args, err := windows.UTF16PtrFromString(params)
	if err != nil {
		return err
	}
	show := int32(1)
	r, _, callErr := shellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(args)),
		0,
		uintptr(show),
	)
	if r <= 32 {
		if callErr != nil && !errors.Is(callErr, windows.ERROR_SUCCESS) {
			return fmt.Errorf("request elevation: %w", callErr)
		}
		return fmt.Errorf("request elevation failed with code %d", r)
	}
	os.Exit(0)
	return nil
}

var (
	shell32        = windows.NewLazySystemDLL("shell32.dll")
	shellExecuteW  = shell32.NewProc("ShellExecuteW")
	advapi32       = windows.NewLazySystemDLL("advapi32.dll")
	checkTokenProc = advapi32.NewProc("CheckTokenMembership")
)

func isAdmin() (bool, error) {
	adminSid, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false, err
	}
	var isMember int32
	r, _, callErr := checkTokenProc.Call(
		0,
		uintptr(unsafe.Pointer(adminSid)),
		uintptr(unsafe.Pointer(&isMember)),
	)
	if r == 0 {
		return false, callErr
	}
	return isMember != 0, nil
}

func quoteArgs(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "" || strings.ContainsAny(arg, " \t\"") {
			arg = `"` + strings.ReplaceAll(arg, `"`, `\"`) + `"`
		}
		quoted = append(quoted, arg)
	}
	return strings.Join(quoted, " ")
}
