package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"eino-cli/backend/sandbox"
)

const autoDreamShellDenied = "auto-dream shell only allows read-only commands: ls, pwd, rg, grep"

func isReadOnlyShellCommand(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" || strings.ContainsAny(command, ";&|><`$") {
		return false
	}
	name := strings.Fields(command)[0]
	switch name {
	case "ls", "pwd", "rg", "grep":
		return true
	}
	return false
}

func GetAutoDreamShellTool(sandboxManager sandbox.SandboxManager) (tool.BaseTool, error) {
	return utils.InferTool("shell", shellToolDesc,
		func(ctx context.Context, in shellArgs) (string, error) {
			if !isReadOnlyShellCommand(in.Command) {
				return autoDreamShellDenied, nil
			}
			if sb := getSandbox(ctx, sandboxManager); sb != nil {
				return sb.ExecuteCommand(ctx, in.Command)
			}
			return runShell(resolveRoot(), in)
		})
}

func GetAutoDreamWriteFileTool(memoryRoot string) (tool.BaseTool, error) {
	return utils.InferTool(filesystem.ToolNameWriteFile, filesystem.WriteFileToolDesc,
		func(_ context.Context, in writeFileArgs) (string, error) {
			path, err := getAutoDreamPath(memoryRoot, in.FilePath)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(path, []byte(in.Content), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("Updated file %s", path), nil
		})
}

func GetAutoDreamEditFileTool(memoryRoot string) (tool.BaseTool, error) {
	return utils.InferTool(filesystem.ToolNameEditFile, filesystem.EditFileToolDesc,
		func(_ context.Context, in editFileArgs) (string, error) {
			if in.OldString == "" {
				return "", fmt.Errorf("old_string must not be empty")
			}
			path, err := getAutoDreamPath(memoryRoot, in.FilePath)
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			updated, err := applyEditReplacement(string(data), in.OldString, in.NewString, in.ReplaceAll)
			if err != nil {
				return "", err
			}
			if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("Successfully replaced the string in '%s'", path), nil
		})
}

func getAutoDreamPath(memoryRoot, inputPath string) (string, error) {
	path := inputPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(memoryRoot, path)
	}
	if !isInsidePath(memoryRoot, path) {
		return "", fmt.Errorf("auto-dream may only write inside %s", memoryRoot)
	}
	return filepath.Clean(path), nil
}

func isInsidePath(root, path string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(rootAbs), filepath.Clean(pathAbs))
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}
