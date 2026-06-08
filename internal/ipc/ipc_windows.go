//go:build windows

package ipc

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

const windowsSocketPath = `\\.\pipe\ProtectedPrefix\Administrators\FlexConnect\flexconnectd`
const windowsPipeSecurityDescriptor = "O:BAG:BAD:PAI(A;OICI;GWGR;;;BU)(A;OICI;GWGR;;;SY)"

func DefaultSocketPath() string {
	return windowsSocketPath
}

func Listen(path string) (net.Listener, error) {
	return winio.ListenPipe(
		path,
		&winio.PipeConfig{
			SecurityDescriptor: windowsPipeSecurityDescriptor,
			InputBufferSize:    256 * 1024,
			OutputBufferSize:   256 * 1024,
		},
	)
}

func DialContext(ctx context.Context, path string) (net.Conn, error) {
	return winio.DialPipeAccessImpLevel(
		ctx,
		path,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		winio.PipeImpLevelIdentification,
	)
}
