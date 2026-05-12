// ignore_security_alert_file RCE
package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

type executeArgs struct {
	Command string `json:"command" jsonschema:"description=The command to execute"`
}

// GetExecuteTool returns the "execute" tool. Runs the command via bash -lc
// in root's resolved CWD; combines stdout+stderr; appends a failure marker
// on non-zero exit. Non-ExitError errors (e.g. bash not on PATH) propagate
// as Go errors.
func GetExecuteTool(root string) (tool.BaseTool, error) {
	cwd := resolveRoot(root)
	return utils.InferTool(filesystem.ToolNameExecute, filesystem.ExecuteToolDesc,
		func(ctx context.Context, in executeArgs) (string, error) {
			cmd := exec.CommandContext(ctx, "bash", "-lc", in.Command)
			cmd.Dir = cwd
			out, err := cmd.CombinedOutput()
			exitCode := 0
			if err != nil {
				exitErr, ok := err.(*exec.ExitError)
				if !ok {
					return "", err
				}
				exitCode = exitErr.ExitCode()
			}
			return formatExecuteOutput(string(out), exitCode), nil
		})
}

// formatExecuteOutput mirrors eino's convExecuteResponse: append a failure
// tag on non-zero exit; return the canned "no output" message only when
// the command both succeeded and produced nothing.
func formatExecuteOutput(output string, exitCode int) string {
	parts := []string{output}
	if exitCode != 0 {
		parts = append(parts, fmt.Sprintf("[Command failed with exit code %d]", exitCode))
	}
	result := strings.Join(parts, "\n")
	if result == "" && exitCode == 0 {
		return "[Command executed successfully with no output]"
	}
	return result
}
