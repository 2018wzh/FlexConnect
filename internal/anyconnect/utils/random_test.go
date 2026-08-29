package utils

import (
	"errors"
	"testing"
)

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

func TestMasterSecretFailsWithRandomSource(t *testing.T) {
	want := errors.New("random source unavailable")
	secret, err := makeMasterSecretFrom(errorReader{err: want})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	if secret != nil {
		t.Fatalf("secret = %x, want nil", secret)
	}
}
