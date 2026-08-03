//go:build !windows && !linux && !darwin

package osnet

import (
	"context"
	"errors"
	"io"
)

func GetUnderlaySnapshot(context.Context, string) (UnderlaySnapshot, error) {
	return UnderlaySnapshot{}, errors.New("underlay snapshots are unsupported on this platform")
}

func newUnderlayNotifier(func()) (io.Closer, error) {
	return nil, errors.New("underlay notifications unavailable; using snapshot polling")
}
