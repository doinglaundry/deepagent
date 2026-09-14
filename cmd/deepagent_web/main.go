package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"eino-cli/host/cli"
	"eino-cli/host/web"
	"eino-cli/manager"
)

func main() {
	config := flag.String("config", "yaml/deepagent.yaml", "shared Manager YAML")
	addr := flag.String("addr", ":8080", "HTTP listen address")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	mcfg, err := cli.LoadManagerConfig(*config)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	m, err := manager.New(ctx, mcfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer m.Close()
	s := web.New(m)
	h := &http.Server{Addr: *addr, Handler: s.Handler()}
	go func() { <-ctx.Done(); _ = h.Shutdown(context.Background()) }()
	slog.Info("DeepAgent UI listening", "addr", *addr)
	if err := h.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
