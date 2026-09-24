package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"eino-cli/deepagent/core/backend"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type readLintsArgs struct {
	Paths []string `json:"paths,omitempty"`
}
type readLintsTool struct {
	workspace backend.Workspace
	commands  backend.CommandService
}

func NewReadLintsTool(workspace backend.Workspace, commands backend.CommandService) (tool.BaseTool, error) {
	if workspace == nil || commands == nil {
		return nil, fmt.Errorf("workspace and command service are required")
	}
	return &readLintsTool{workspace: workspace, commands: commands}, nil
}

// Go diagnostics execute project tests and therefore require the same approval
// boundary as other project commands.
func (*readLintsTool) RequiresApproval() bool { return true }
func (*readLintsTool) Info(context.Context) (*schema.ToolInfo, error) {
	return toolInfo("read_lints", "Run Go diagnostics for workspace packages.", map[string]*schema.ParameterInfo{"paths": {Type: schema.Array, ElemInfo: &schema.ParameterInfo{Type: schema.String}}})
}
func (t *readLintsTool) InvokableRun(ctx context.Context, raw string, _ ...tool.Option) (string, error) {
	var input readLintsArgs
	if err := decodeToolArgs(raw, &input); err != nil {
		return "", err
	}
	args := []string{}
	if len(input.Paths) == 0 {
		args = append(args, "./...")
	}
	seen := map[string]bool{}
	for _, path := range input.Paths {
		resolved, err := t.workspace.Resolve(ctx, path, false)
		if err != nil {
			return "", err
		}
		if _, err := t.workspace.List(ctx, path); err != nil {
			if filepath.Ext(resolved) != ".go" {
				continue
			}
			resolved = filepath.Dir(resolved)
		}
		relative, err := filepath.Rel(t.workspace.Root(), resolved)
		if err != nil || !filepath.IsLocal(relative) {
			return "", fmt.Errorf("diagnostic path outside workspace")
		}
		pkg := "./..."
		if relative != "." {
			pkg = "./" + filepath.ToSlash(relative) + "/..."
		}
		if !seen[pkg] {
			seen[pkg] = true
			args = append(args, pkg)
		}
	}
	if len(args) == 0 {
		return "No diagnostics provider for the selected paths", nil
	}
	command := "go test"
	for _, arg := range args {
		command += " '" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
	}
	result, err := t.commands.Execute(ctx, backend.CommandRequest{Command: command, Timeout: 2 * time.Minute, MaxOutputBytes: 64 << 10})
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", fmt.Errorf("diagnostics command returned no result")
	}
	if result.TimedOut {
		return "", fmt.Errorf("diagnostics timed out: %s", result.Output)
	}
	if result.ExitCode != 0 {
		return strings.TrimSpace(result.Output), nil
	}
	return "No diagnostics", nil
}
