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
	workspace filesystempkg.Filesystem
	commands  filesystempkg.CommandService
}

func NewReadLintsTool(workspace filesystempkg.Filesystem, commands filesystempkg.CommandService) (tool.BaseTool, error) {
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
	err := json.Unmarshal([]byte(raw), &input)
	if err != nil {
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
		_, listErr := t.workspace.List(ctx, path)
		if listErr != nil {
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
	result, err := t.commands.Execute(ctx, filesystempkg.CommandRequest{Command: command, Timeout: 2 * time.Minute, MaxOutputBytes: 64 << 10})
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
