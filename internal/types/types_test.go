package types

import (
	"errors"
	"testing"
)

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestNewIDFailsWhenCryptographicRandomSourceFails(t *testing.T) {
	want := errors.New("random source unavailable")
	if id, err := newIDFrom(failingReader{err: want}); !errors.Is(err, want) || id != "" {
		t.Fatalf("newIDFrom = %q, %v", id, err)
	}
}
