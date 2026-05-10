// Command eino-cli is the Bubbletea chat front-end over eino.Runtime.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"eino-cli/backend/cli/tui"
	"eino-cli/backend/config"
	"eino-cli/backend/runtime/eino"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	rt, err := eino.NewDeepAgentRuntime(context.Background(), cfg)
	if err != nil {
		log.Fatalf("build runtime: %v", err)
	}

	if err := tui.Run(rt); err != nil {
		fmt.Fprintf(os.Stderr, "tui exited: %v\n", err)
		os.Exit(1)
	}
}
