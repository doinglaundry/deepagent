// Command eino-cli: --mode=cli (default) or --mode=server runs the gateway.
package main

import (
	"context"
	"flag"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"eino-cli/backend/cli/tui"
	"eino-cli/backend/config"
	"eino-cli/backend/gateway"
	"eino-cli/backend/runtime/eino"
	"eino-cli/backend/sandbox"

	// init()-only: register sandbox factories.
	_ "eino-cli/backend/sandbox/aio"
	_ "eino-cli/backend/sandbox/local"
)

func main() {
	args := os.Args[1:]
	root, mode, addr, err := parseFlags(args, os.Getenv, os.Getwd)
	if err != nil {
		log.Fatalf("parse flags: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		log.Fatalf("enter root: %v", err)
	}

	cfg, err := config.Load(root)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	config.SetLogLevel(cfg)

	manager, err := sandbox.NewSandboxManager(cfg)
	if err != nil {
		log.Fatalf("build sandbox manager: %v", err)
	}
	sandbox.SetDefault(manager)
	defer sandbox.ShutdownDefault()

	switch mode {
	case "server":
		runServer(cfg, addr)
	default:
		runCLI(cfg)
	}
}

func runCLI(cfg *config.Config) {
	rt, err := eino.NewDeepAgentRuntime(context.Background(), cfg)
	if err != nil {
		log.Fatalf("build runtime: %v", err)
	}
	if err := tui.Run(rt, cfg); err != nil {
		os.Exit(1)
	}
}

func runServer(cfg *config.Config, addr string) {
	router := eino.NewRouter(cfg)
	defer router.Shutdown()

	srv := gateway.New(cfg, router)
	log.Printf("eino-cli gateway listening on %s", addr)
	if err := srv.ListenAndServe(addr); err != nil {
		log.Fatalf("gateway: %v", err)
	}
}

// parseFlags reads --root / --mode / --addr; getenv/getwd are passed in for tests.
func parseFlags(args []string, getenv func(string) string, getwd func() (string, error)) (root, mode, addr string, err error) {
	flags := flag.NewFlagSet("eino-cli", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	rootFlag := flags.String("root", "", "LLM repository root")
	modeFlag := flags.String("mode", "cli", "Run mode: cli or server")
	addrFlag := flags.String("addr", ":8000", "Server bind address (mode=server only)")
	if err = flags.Parse(args); err != nil {
		return "", "", "", err
	}

	root = strings.TrimSpace(*rootFlag)
	if root == "" {
		root = strings.TrimSpace(getenv("SGADK_ROOT"))
	}
	if root == "" {
		wd, err := getwd()
		if err != nil {
			return "", "", "", err
		}
		root = wd
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", "", "", err
	}
	mode = strings.TrimSpace(*modeFlag)
	if mode == "" {
		mode = "cli"
	}
	addr = strings.TrimSpace(*addrFlag)
	if addr == "" {
		addr = ":8000"
	}
	return root, mode, addr, nil
}
