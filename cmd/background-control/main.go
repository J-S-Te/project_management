package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/j-s-te/project-management/internal/background"
	"os"
	"time"
)

func main() {
	action := flag.String("action", "inspect", "fixed control action")
	operation := flag.String("operation-id", "", "safe operation identifier")
	socket := flag.String("socket", "", "local private control socket")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments")
		os.Exit(2)
	}
	if *socket == "" {
		*socket = os.Getenv("PROJECT_BACKGROUND_CONTROL_SOCKET")
		if *socket == "" {
			*socket = "/tmp/project-background/control.sock"
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	state, err := background.SendControl(ctx, *socket, background.ControlRequest{Action: *action, OperationID: *operation})
	if state.Protocol == 0 {
		state.Protocol = 1
		state.ErrorCode = "CONTROL_UNAVAILABLE"
	}
	if e := json.NewEncoder(os.Stdout).Encode(state); e != nil {
		fmt.Fprintln(os.Stderr, "control output failed")
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "background control blocked")
		os.Exit(1)
	}
}
