package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/gen2brain/beeep"

	"flexconnect/client/local"
	"flexconnect/client/systray"
	"flexconnect/internal/ipc"
)

func main() {
	socket := flag.String("socket", ipc.DefaultSocketPath(), "daemon socket or named pipe path")
	flag.Parse()

	client := &local.Client{Socket: *socket}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := client.Status(ctx); err != nil {
		showStartupError("FlexConnect is not running. Start flexconnectd before opening the tray.", err)
		os.Exit(1)
	}

	menu := &systray.Menu{Client: client}
	menu.Run()
}

func showStartupError(message string, err error) {
	title := "FlexConnect"
	full := fmt.Sprintf("%s\n\nDetails: %v", message, err)
	if err := beeep.Alert(title, full, ""); err != nil {
		fmt.Fprintln(os.Stderr, full)
	}
}

