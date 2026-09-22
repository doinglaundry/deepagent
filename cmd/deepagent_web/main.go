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

	"eino-cli/deepagent/host/web"
	deepmanager "eino-cli/deepagent/manager"
)

func main() {
	config := flag.String("config", "yaml/deepagent.yaml", "shared Manager YAML")
	addr := flag.String("addr", ":8080", "HTTP listen address")
	root := flag.String("root", ".", "workspace root exposed to DeepAgent threads")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	mcfg, err := deepmanager.LoadConfig(*config)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	m, err := deepmanager.Open(ctx, mcfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	s := web.New(m, *root)
	h := &http.Server{Addr: *addr, Handler: s.Handler()}
	go func() { <-ctx.Done(); _ = h.Shutdown(context.Background()) }()
	slog.Info("DeepAgent UI listening", "addr", *addr)
	if err := h.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
