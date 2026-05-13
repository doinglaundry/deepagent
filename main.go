// Command eino-cli is the Bubbletea chat front-end over eino.Runtime.
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
	"eino-cli/backend/runtime/eino"
)

func main() {
	root, err := resolveRoot(os.Args[1:], os.Getenv, os.Getwd)
	if err != nil {
		log.Fatalf("get root: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		log.Fatalf("enter root: %v", err)
	}

	// 获取配置
	cfg, err := config.Load(root)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// 把 cfg.LogLevel 装到 slog 默认 handler 上,Debug 才能真正出来。
	// 必须在任何 agent / runtime 起来之前调,确保后续 slog.* 都走同一份配置。
	config.SetLogLevel(cfg)

	//构建runtime
	runtime, err := eino.NewDeepAgentRuntime(context.Background(), cfg)
	if err != nil {
		log.Fatalf("build runtime: %v", err)
	}

	// runtime运行 + cli渲染
	if err := tui.Run(runtime, cfg); err != nil {
		os.Exit(1)
	}
}

func resolveRoot(args []string, getenv func(string) string, getwd func() (string, error)) (string, error) {
	flags := flag.NewFlagSet("sgadk", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	rootFlag := flags.String("root", "", "LLM repository root")
	if err := flags.Parse(args); err != nil {
		return "", err
	}

	root := strings.TrimSpace(*rootFlag)
	if root == "" {
		root = strings.TrimSpace(getenv("SGADK_ROOT"))
	}
	if root == "" {
		wd, err := getwd()
		if err != nil {
			return "", err
		}
		root = wd
	}
	return filepath.Abs(root)
}
