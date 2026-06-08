package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"flexconnect/internal/apiserver"
	"flexconnect/internal/appd"
	"flexconnect/internal/ipc"
	"flexconnect/internal/logging"
	"flexconnect/internal/router"
	"flexconnect/internal/secret"
	storefile "flexconnect/internal/store/file"
	vpnac "flexconnect/internal/vpn/anyconnect"
)

func main() {
	opts := daemonOptions{}
	flag.StringVar(&opts.socket, "socket", ipc.DefaultSocketPath(), "daemon socket or named pipe path")
	flag.StringVar(&opts.state, "state", defaultStatePath(), "path to state file")
	verbose := flag.Bool("v", false, "enable verbose debug logging")
	verboseLong := flag.Bool("verbose", false, "same as -v")
	flag.Parse()
	opts.verbose = *verbose || *verboseLong

	if isWindowsService() {
		if err := runWindowsService(opts); err != nil {
			logging.Init(os.Stdout, logging.LevelInfo, true)
			logging.WithComponent("flexconnectd").Fatalf("%v", err)
		}
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := runDaemon(ctx, opts); err != nil {
		logging.Init(os.Stdout, logging.LevelInfo, true)
		logging.WithComponent("flexconnectd").Fatalf("%v", err)
	}
}

type daemonOptions struct {
	socket  string
	state   string
	verbose bool
}

func runDaemon(ctx context.Context, opts daemonOptions) error {
	startedAt := time.Now()
	appd.SetDebug(opts.verbose)
	logging.Init(os.Stdout, logging.LevelInfo, true)
	logging.SetLevel(condLevel(opts.verbose))
	serverLog := logging.WithComponent("flexconnectd")
	serverLog.Printf("starting pid=%d at=%s", os.Getpid(), startedAt.Format(time.RFC3339Nano))
	if opts.verbose {
		serverLog.Printf("verbose logging enabled")
	}

	if err := ensureElevated(); err != nil {
		return err
	}
	serverLog.Printf("elevation check passed")
	serverLog.Printf("configuration backend=anyconnect socket=%s state=%s", opts.socket, opts.state)

	service, err := newService(opts.state)
	if err != nil {
		return err
	}
	serverLog.Printf("service initialized")

	listener, err := ipc.Listen(opts.socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	serverLog.Printf("ipc listener ready at %s", opts.socket)

	server := &http.Server{Handler: apiserver.New(service).Handler()}
	errCh := make(chan error, 1)
	go func() {
		serverLog.Printf("http server serving")
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		serverLog.Printf("shutdown requested after %s", time.Since(startedAt))
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}

	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}

func condLevel(verbose bool) logging.Level {
	if verbose {
		return logging.LevelDebug
	}
	return logging.LevelInfo
}

func newService(statePath string) (*appd.Service, error) {
	store := storefile.New(statePath)
	secrets := secret.NewKeyringStore("flexconnect")
	return appd.New(store, secrets, vpnac.New(), router.DefaultPlanner{})
}
