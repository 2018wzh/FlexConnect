//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type instanceLock struct {
	file *os.File
}

func acquireInstanceLock(name string) (*instanceLock, bool, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return nil, false, err
	}
	dir := filepath.Join(cacheDir, "flexconnect")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, false, err
	}
	path := filepath.Join(dir, lockFileName(name))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, false, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
			return nil, true, nil
		}
		return nil, false, err
	}
	return &instanceLock{file: file}, false, nil
}

func lockFileName(name string) string {
	if name == "FlexConnectFlexTray" {
		return "flextray.lock"
	}
	return strings.ToLower(name) + ".lock"
}

func (l *instanceLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	if closeErr := l.file.Close(); err == nil {
		err = closeErr
	}
	return err
}
