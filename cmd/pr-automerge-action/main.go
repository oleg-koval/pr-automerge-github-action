package main

import (
	"context"
	"log"
	"os"

	"github.com/oleg-koval/pr-automerge-github-action/internal/action"
)

func main() {
	logger := log.New(os.Stdout, "", 0)
	if err := action.Run(context.Background(), os.Environ(), logger); err != nil {
		logger.Printf("error: %v", err)
		os.Exit(1)
	}
}
