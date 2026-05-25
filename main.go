// Command eino-cli runs the interactive CLI agent.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"eino-cli/backend/cli/tui"
	"eino-cli/backend/config"
	"eino-cli/backend/runtime/deepagent"
	"eino-cli/backend/sandbox"
	"eino-cli/backend/sandbox/aio"
	"eino-cli/backend/sandbox/local"
	"eino-cli/backend/session"
)

func main() {
	args := os.Args[1:]
	root, err := parseFlags(args)
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

	ctx := context.Background()
	sessionID, err := session.StartSession(ctx)
	if err != nil {
		log.Fatalf("start session: %v", err)
	}

	sandboxManager, err := buildSandboxManager(cfg, sessionID)
	if err != nil {
		log.Fatalf("build sandbox manager: %v", err)
	}
	sandbox.SetDefault(sandboxManager)
	defer sandbox.ShutdownDefault()

	runCLI(cfg, sessionID)
}

func buildSandboxManager(cfg *config.Config, sessionID string) (sandbox.SandboxManager, error) {
	use := ""
	if cfg != nil {
		use = strings.TrimSpace(cfg.Sandbox.Use)
	}
	switch use {
	case "", "local":
		return local.New(sessionID)
	case "aio":
		return aio.New(cfg, sessionID)
	default:
		return nil, fmt.Errorf("sandbox: unknown sandbox.use %q (allowed: local, aio)", use)
	}
}

func runCLI(cfg *config.Config, sessionID string) {
	if err := resetAgentMessagesLog(); err != nil {
		log.Fatalf("reset agent messages log: %v", err)
	}

	rt, err := deepagent.NewRuntime(context.Background(), cfg)
	if err != nil {
		log.Fatalf("build runtime: %v", err)
	}
	if err := tui.Run(rt, sessionID, cfg); err != nil {
		os.Exit(1)
	}
}

func resetAgentMessagesLog() error {
	path := config.AgentMessagesLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, nil, 0o644)
}

func parseFlags(args []string) (root string, err error) {
	flags := flag.NewFlagSet("eino-cli", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	rootFlag := flags.String("root", "", "LLM repository root")

	if err := flags.Parse(args); err != nil {
		return "", err
	}

	root, err = getRoot(*rootFlag)
	if err != nil {
		return "", err
	}
	return root, nil
}

func getRoot(flagRoot string) (string, error) {
	root := strings.TrimSpace(flagRoot)
	if root == "" {
		root = strings.TrimSpace(os.Getenv("SGADK_ROOT"))
	}
	if root == "" {
		return os.Getwd()
	}
	return filepath.Abs(root)
}
