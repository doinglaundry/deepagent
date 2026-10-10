package main

import (
	"context"
	"eino-cli/deepagent/appconfig"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	daldb "eino-cli/deepagent/dal/db"
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
	cfg, err := appconfig.Load(*config)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	m, err := deepmanager.Open(ctx, cfg.Manager)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	s := web.New(m, *root)
	if cfg.LocalModel != nil {
		localModelDAO := daldb.NewLocalModelDAO(m.DB(), cfg.LocalModel.ModelName)
		err = localModelDAO.MigrateSchema(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		s.EnableLocalModel(localModelDAO, cfg.LocalModel.EnableAutoTraining)
	}
	h := &http.Server{Addr: *addr, Handler: s.Handler(), BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() { <-ctx.Done(); _ = h.Shutdown(context.Background()) }()
	slog.Info("DeepAgent UI listening", "addr", *addr)
	listenAndServeErr := h.ListenAndServe()
	if listenAndServeErr != nil && listenAndServeErr != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, listenAndServeErr)
		os.Exit(1)
	}
}
