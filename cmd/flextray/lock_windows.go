//go:build windows

package main

import (
	"golang.org/x/sys/windows"
)

type instanceLock struct {
	handle windows.Handle
}

func acquireInstanceLock(name string) (*instanceLock, bool, error) {
	name16, err := windows.UTF16PtrFromString(`Local\` + name)
	if err != nil {
		return nil, false, err
	}
	handle, err := windows.CreateMutex(nil, false, name16)
	if err == windows.ERROR_ALREADY_EXISTS {
		_ = windows.CloseHandle(handle)
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &instanceLock{handle: handle}, false, nil
}

func (l *instanceLock) Close() error {
	if l == nil || l.handle == 0 {
		return nil
	}
	return windows.CloseHandle(l.handle)
}
