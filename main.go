// Command eino-cli is the Bubbletea chat front-end over eino.Runtime.
package main

import (
	"context"
	"log"
	"os"

	"eino-cli/backend/cli/tui"
	"eino-cli/backend/config"
	"eino-cli/backend/runtime/eino"
)

func main() {

	// 获取根目录
	root, err := os.Getwd()
	if err != nil {
		log.Fatalf("get root: %v", err)
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
