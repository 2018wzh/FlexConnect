//go:build windows

package ipc

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestNamedPipeCapturesClientIdentity(t *testing.T) {
	pipe := fmt.Sprintf(`\\.\pipe\flexconnect-identity-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	listener, err := listenPipeWithSecurity(pipe, "D:P(A;;GA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	result := make(chan Identity, 1)
	errs := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			errs <- err
			return
		}
		defer conn.Close()
		identity, ok := IdentityFromConn(conn)
		if !ok {
			errs <- fmt.Errorf("accepted connection has no identity")
			return
		}
		result <- identity
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := DialContext(ctx, pipe)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case err := <-errs:
		t.Fatal(err)
	case identity := <-result:
		if identity.ID == "" {
			t.Fatal("empty SID")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
