package ipc

import (
	"context"
	"net"
)

const LocalAPIHost = "local-flexconnectd.sock"

type DialFunc func(context.Context, string) (net.Conn, error)
