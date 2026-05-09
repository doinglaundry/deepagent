// Command eino-cli is a Bubbletea-based interactive chat
// front-end on top of the eino.Runtime. Run it with `go run .`
// from the repo root, or build a binary via `go build -o eino`.
//
// The package layout intentionally keeps the binary thin: this
// file is just a wiring root. All UI lives in backend/cli/tui,
// the agent stack lives in backend/agent + backend/runtime/eino,
// and configuration loading lives in backend/config.
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
