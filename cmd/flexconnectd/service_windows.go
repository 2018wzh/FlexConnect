//go:build windows

package main

import (
	"context"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

const windowsServiceName = "FlexConnect"

func isWindowsService() bool {
	ok, err := svc.IsWindowsService()
	if err != nil {
		return false
	}
	return ok
}

func runWindowsService(opts daemonOptions) error {
	return svc.Run(windowsServiceName, &daemonService{opts: opts})
}

type daemonService struct {
	opts daemonOptions
}

func (s *daemonService) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown

	changes <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErrCh := make(chan error, 1)
	readyCh := make(chan error, 1)
	go func() {
		runErrCh <- runDaemonReady(ctx, s.opts, readyCh)
	}()

	select {
	case err := <-readyCh:
		if err != nil {
			cancel()
			return false, uint32(windows.ERROR_SERVICE_SPECIFIC_ERROR)
		}
	case err := <-runErrCh:
		if err != nil {
			return false, uint32(windows.ERROR_SERVICE_SPECIFIC_ERROR)
		}
		return false, windows.NO_ERROR
	}

	changes <- svc.Status{State: svc.Running, Accepts: accepts}

	stopping := false
	for {
		select {
		case err := <-runErrCh:
			if err != nil {
				return false, uint32(windows.ERROR_SERVICE_SPECIFIC_ERROR)
			}
			return false, windows.NO_ERROR
		case req := <-requests:
			switch req.Cmd {
			case svc.Stop, svc.Shutdown:
				if !stopping {
					stopping = true
					changes <- svc.Status{State: svc.StopPending}
					cancel()
				}
			case svc.Interrogate:
				changes <- req.CurrentStatus
			}
		}
	}
}
