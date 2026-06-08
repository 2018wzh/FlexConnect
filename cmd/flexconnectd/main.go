package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
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
	socket := flag.String("socket", ipc.DefaultSocketPath(), "daemon socket or named pipe path")
	state := flag.String("state", defaultStatePath(), "path to state file")
	verbose := flag.Bool("v", false, "enable verbose debug logging")
	verboseLong := flag.Bool("verbose", false, "same as -v")
	flag.Parse()
	startedAt := time.Now()
	appd.SetDebug(*verbose || *verboseLong)
	logging.Init(os.Stdout, logging.LevelInfo, true)
	logging.SetLevel(condLevel(*verbose || *verboseLong))
	serverLog := logging.WithComponent("flexconnectd")
	serverLog.Printf("starting pid=%d at=%s", os.Getpid(), startedAt.Format(time.RFC3339Nano))
	if *verbose || *verboseLong {
		serverLog.Printf("verbose logging enabled")
	}

	if err := ensureElevated(); err != nil {
		serverLog.Fatalf("%v", err)
	}
	serverLog.Printf("elevation check passed")

	serverLog.Printf("configuration backend=anyconnect socket=%s state=%s", *socket, *state)
	service, err := newService(*state)
	if err != nil {
		serverLog.Fatalf("%v", err)
	}
	serverLog.Printf("service initialized")

	listener, err := ipc.Listen(*socket)
	if err != nil {
		serverLog.Fatalf("%v", err)
	}
	defer listener.Close()
	serverLog.Printf("ipc listener ready at %s", *socket)

	server := &http.Server{Handler: apiserver.New(service).Handler()}
	go func() {
		serverLog.Printf("http server serving")
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverLog.Fatalf("%v", err)
		}
	}()
	// ensure cleanup when daemon exits unexpectedly
	defer func() {
		serverLog.Printf("shutting down after %s", time.Since(startedAt))
		_ = server.Shutdown(context.Background())
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	stopSig := <-sig
	serverLog.Printf("shutdown signal received: %v", stopSig)
	_ = server.Shutdown(context.Background())
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

func defaultStatePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "flexconnect-state.json"
	}
	return filepath.Join(dir, "FlexConnect", "state.json")
}
