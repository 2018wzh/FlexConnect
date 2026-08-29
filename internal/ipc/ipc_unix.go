//go:build !windows

package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

func Listen(path string) (net.Listener, error) {
	if path == "" {
		return nil, errors.New("unix socket path is empty")
	}

	sockDir := filepath.Dir(path)
	if info, err := os.Stat(sockDir); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat unix socket directory %s: %w", sockDir, err)
		}
		if err := os.MkdirAll(sockDir, 0o750); err != nil {
			return nil, fmt.Errorf("create unix socket directory %s: %w", sockDir, err)
		}
	} else if !info.IsDir() {
		return nil, fmt.Errorf("unix socket parent is not a directory: %s", sockDir)
	}

	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("refusing to replace non-socket path: %s", path)
		}
		conn, dialErr := net.DialTimeout("unix", path, time.Second)
		if dialErr == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("%s: address already in use", path)
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) && !errors.Is(dialErr, syscall.ENOENT) {
			return nil, fmt.Errorf("inspect existing unix socket %s: %w", path, dialErr)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("remove stale unix socket %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect unix socket %s: %w", path, err)
	}

	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on unix socket %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o660); err != nil {
		_ = ln.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("set unix socket permissions %s: %w", path, err)
	}
	created, err := os.Lstat(path)
	if err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("inspect created unix socket %s: %w", path, err)
	}
	return &cleanupListener{Listener: identityListener{Listener: ln}, path: path, created: created}, nil
}

type cleanupListener struct {
	net.Listener
	path    string
	created os.FileInfo
	once    sync.Once
	err     error
}

func (l *cleanupListener) Close() error {
	l.once.Do(func() {
		closeErr := l.Listener.Close()
		current, statErr := os.Lstat(l.path)
		switch {
		case statErr == nil && os.SameFile(l.created, current):
			if removeErr := os.Remove(l.path); removeErr != nil && !os.IsNotExist(removeErr) {
				l.err = fmt.Errorf("remove unix socket %s: %w", l.path, removeErr)
			}
		case statErr != nil && !os.IsNotExist(statErr):
			l.err = fmt.Errorf("inspect unix socket during close %s: %w", l.path, statErr)
		}
		if l.err == nil {
			l.err = closeErr
		}
	})
	return l.err
}

func DialContext(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", path)
}
