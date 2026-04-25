package eino

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"

	"eino-cli/internal/tools"
	"eino-cli/internal/tools/execute"
)

type readToolInput struct {
	Path string `json:"path"`
}

type lsToolInput struct {
	Path string `json:"path,omitempty"`
}

func buildRuntimeTools() []tool.BaseTool {
	exec := execute.New()
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}

	readTool, err := toolutils.InferTool("read", "Read a local file", func(ctx context.Context, in readToolInput) (string, error) {
		path := strings.TrimSpace(in.Path)
		if path == "" {
			return "", fmt.Errorf("path is required")
		}
		result, runErr := exec.Execute(tools.Tool{Name: "read"}, []string{path}, cwd)
		if runErr != nil {
			return result.Output, runErr
		}
		return result.Output, nil
	})
	if err != nil {
		return []tool.BaseTool{}
	}

	lsTool, err := toolutils.InferTool("ls", "List a directory", func(ctx context.Context, in lsToolInput) (string, error) {
		args := []string{}
		if strings.TrimSpace(in.Path) != "" {
			args = append(args, strings.TrimSpace(in.Path))
		}
		result, runErr := exec.Execute(tools.Tool{Name: "ls"}, args, cwd)
		if runErr != nil {
			return result.Output, runErr
		}
		return result.Output, nil
	})
	if err != nil {
		return []tool.BaseTool{readTool}
	}

	return []tool.BaseTool{readTool, lsTool}
}
