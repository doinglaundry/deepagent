package main

import (
	"context"
	"eino-cli/deepagent/appconfig"
	worker "eino-cli/deepagent/worker"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	config := flag.String("config", "yaml/deepagent.yaml", "Worker YAML with shared Manager, models, and execution settings")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	c, err := appconfig.Load(*config)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	slog.Info("starting distributed Worker", "default_model", c.DefaultModel)
	if err = worker.Run(ctx, c); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
