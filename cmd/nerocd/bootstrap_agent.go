package main

import (
	"context"
	"errors"
	"os"
	"time"

	"nerocd/internal/serverbootstrap"
)

func runBootstrapAgent(args []string) error {
	if len(args) == 1 && args[0] == "owned" {
		ctx, cancel := context.WithTimeout(context.Background(), 115*time.Second)
		defer cancel()
		return serverbootstrap.ServeOwnedHelper(ctx, version, os.Stdin, os.Stdout)
	}
	if len(args) == 2 && args[0] == "run-owned" {
		return serverbootstrap.RunOwnedAgent(version, args[1])
	}
	return errors.New("invalid owned bootstrap command")
}
