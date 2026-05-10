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

	// 获取配置
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	//构建runtime
	runtime, err := eino.NewDeepAgentRuntime(context.Background(), cfg)
	if err != nil {
		log.Fatalf("build runtime: %v", err)
	}

	// runtime运行 + cli渲染
	if err := tui.Run(runtime); err != nil {
		os.Exit(1)
	}
}
