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

	"eino-cli/backend/sandbox"
)

type executeArgs struct {
	Command string `json:"command" jsonschema:"description=The command to execute"`
}

// GetExecuteTool returns the execute tool; sandbox routes when wired, else bash -lc.
func GetExecuteTool(sandboxManager sandbox.SandboxManager) (tool.BaseTool, error) {
	cwd := resolveRoot()
	return utils.InferTool(filesystem.ToolNameExecute, filesystem.ExecuteToolDesc,
		func(ctx context.Context, in executeArgs) (string, error) {
			if msg, denied := denyOnPlanMode(ctx); denied {
				return msg, nil
			}
			if allowsIsolatedExec(sandboxManager) {
				sb, err := getRequiredSandbox(ctx, sandboxManager)
				if err != nil {
					return "", err
				}
				return sb.ExecuteCommand(ctx, in.Command)
			}
			if msg, denied := denyOnRollbackProtected(ctx); denied {
				return msg, nil
			}
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
