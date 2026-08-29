//go:build windows

package ipc

import (
	"context"
	"fmt"
	"net"
	"runtime"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

const windowsSocketPath = `\\.\pipe\ProtectedPrefix\Administrators\FlexConnect\flexconnectd`
const windowsPipeSecurityDescriptor = "O:BAG:BAD:PAI(A;OICI;GWGR;;;BU)(A;OICI;GWGR;;;SY)"

func DefaultSocketPath() string {
	return windowsSocketPath
}

func Listen(path string) (net.Listener, error) {
	return listenPipeWithSecurity(path, windowsPipeSecurityDescriptor)
}

func listenPipeWithSecurity(path, securityDescriptor string) (net.Listener, error) {
	listener, err := winio.ListenPipe(
		path,
		&winio.PipeConfig{
			SecurityDescriptor: securityDescriptor,
			InputBufferSize:    256 * 1024,
			OutputBufferSize:   256 * 1024,
		},
	)
	if err != nil {
		return nil, err
	}
	return identityListener{Listener: listener}, nil
}

func DialContext(ctx context.Context, path string) (net.Conn, error) {
	return winio.DialPipeAccessImpLevel(
		ctx,
		path,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		winio.PipeImpLevelIdentification,
	)
}

var (
	advapi32                       = windows.NewLazySystemDLL("advapi32.dll")
	procImpersonateNamedPipeClient = advapi32.NewProc("ImpersonateNamedPipeClient")
)

func platformConnIdentity(conn net.Conn) (Identity, error) {
	fd, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return Identity{}, fmt.Errorf("named pipe connection does not expose a handle")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	result, _, callErr := procImpersonateNamedPipeClient.Call(fd.Fd())
	if result == 0 {
		return Identity{}, fmt.Errorf("impersonate named pipe client: %w", callErr)
	}
	var token windows.Token
	err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &token)
	revertErr := windows.RevertToSelf()
	if err != nil {
		return Identity{}, fmt.Errorf("open named pipe client token: %w", err)
	}
	defer token.Close()
	if revertErr != nil {
		return Identity{}, fmt.Errorf("revert named pipe impersonation: %w", revertErr)
	}
	user, err := token.GetTokenUser()
	if err != nil {
		return Identity{}, fmt.Errorf("read named pipe client SID: %w", err)
	}
	sid := user.User.Sid.String()
	return Identity{ID: sid, System: sid == "S-1-5-18", Elevated: token.IsElevated()}, nil
}
