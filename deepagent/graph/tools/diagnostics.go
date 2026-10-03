package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	filesystempkg "eino-cli/deepagent/graph/filesystem"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type readLintsArgs struct {
	Paths []string `json:"paths,omitempty"`
}

type readLintsTool struct {
	filesystem     filesystempkg.Filesystem
	commandService filesystempkg.CommandService
}

func NewReadLintsTool(filesystem filesystempkg.Filesystem, commandService filesystempkg.CommandService) (ToolDescriptor, error) {
	if filesystem == nil || commandService == nil {
		return ToolDescriptor{}, fmt.Errorf("workspace and command service are required")
	}
	// Go diagnostics execute project tests and require approval, like commands.
	return ToolDescriptor{Tool: &readLintsTool{filesystem: filesystem, commandService: commandService}, RequiresApproval: true}, nil
}

func (*readLintsTool) Info(context.Context) (*schema.ToolInfo, error) {
	return newToolInfo("read_lints", "Run Go diagnostics for workspace packages.", map[string]*schema.ParameterInfo{"paths": {Type: schema.Array, ElemInfo: &schema.ParameterInfo{Type: schema.String}}})
}

func (readLintsTool *readLintsTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	var lintArgs readLintsArgs
	err := json.Unmarshal([]byte(arguments), &lintArgs)
	if err != nil {
		return "", err
	}
	packagePatterns := []string{}
	if len(lintArgs.Paths) == 0 {
		packagePatterns = append(packagePatterns, "./...")
	}
	seenPackages := map[string]bool{}
	for _, path := range lintArgs.Paths {
		resolvedPath, err := readLintsTool.filesystem.Resolve(ctx, path, false)
		if err != nil {
			return "", err
		}
		_, listErr := readLintsTool.filesystem.List(ctx, path)
		if listErr != nil {
			if filepath.Ext(resolvedPath) != ".go" {
				continue
			}
			resolvedPath = filepath.Dir(resolvedPath)
		}
		relativePath, err := filepath.Rel(readLintsTool.filesystem.GetRoot(), resolvedPath)
		if err != nil || !filepath.IsLocal(relativePath) {
			return "", fmt.Errorf("diagnostic path outside workspace")
		}
		packagePattern := "./..."
		if relativePath != "." {
			packagePattern = "./" + filepath.ToSlash(relativePath) + "/..."
		}
		if !seenPackages[packagePattern] {
			seenPackages[packagePattern] = true
			packagePatterns = append(packagePatterns, packagePattern)
		}
	}
	if len(packagePatterns) == 0 {
		return "No diagnostics provider for the selected paths", nil
	}
	command := "go test"
	for _, packageArgument := range packagePatterns {
		command += " '" + strings.ReplaceAll(packageArgument, "'", "'\"'\"'") + "'"
	}
	commandResult, err := readLintsTool.commandService.Execute(ctx, filesystempkg.CommandRequest{Command: command, Timeout: 2 * time.Minute, MaxOutputBytes: 64 << 10})
	if err != nil {
		return "", err
	}
	if commandResult == nil {
		return "", fmt.Errorf("diagnostics command returned no result")
	}
	if commandResult.TimedOut {
		return "", fmt.Errorf("diagnostics timed out: %s", commandResult.Output)
	}
	if commandResult.ExitCode != 0 {
		return strings.TrimSpace(commandResult.Output), nil
	}
	return "No diagnostics", nil
}
