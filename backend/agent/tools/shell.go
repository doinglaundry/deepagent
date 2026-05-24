// ignore_security_alert_file RCE
package tools

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"eino-cli/backend/sandbox"
)

const shellToolDesc = `Execute a shell command in the workspace. Short commands return output directly. Commands still running after timeout_ms continue in the background and return a task_id for await_shell.`

type shellArgs struct {
	Command     string `json:"command" jsonschema:"required,description=Shell command to run"`
	WorkingDir  string `json:"working_directory,omitempty" jsonschema:"description=Working directory; omit for workspace root"`
	TimeoutMS   int    `json:"timeout_ms,omitempty" jsonschema:"description=Foreground wait timeout in milliseconds"`
	Description string `json:"description,omitempty" jsonschema:"description=Short human-readable command description"`
}

func GetShellTool(sandboxManager sandbox.SandboxManager) (tool.BaseTool, error) {
	return utils.InferTool("shell", shellToolDesc,
		func(ctx context.Context, in shellArgs) (string, error) {
			if msg, denied := denyOnPlanMode(ctx); denied {
				return msg, nil
			}
			// Sandbox path is sync; background-job semantics only via host fs.
			if sb := getSandbox(ctx, sandboxManager); allowsIsolatedExec(sandboxManager) && sb != nil {
				return sb.ExecuteCommand(ctx, in.Command)
			}
			if msg, denied := denyOnRollbackProtected(ctx); denied {
				return msg, nil
			}
			return runShell(resolveRoot(), in)
		})
}

func runShell(workingDir string, in shellArgs) (string, error) {
	if strings.TrimSpace(in.Command) == "" {
		return "", fmt.Errorf("command must not be empty")
	}
	if strings.TrimSpace(in.WorkingDir) != "" {
		var err error
		workingDir, err = getResolvedPath(in.WorkingDir)
		if err != nil {
			return "", err
		}
	}
	timeout := time.Duration(in.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	job, done, err := startShellJob(in.Command, workingDir)
	if err != nil {
		return "", err
	}
	select {
	case <-done:
		output, _, exitCode := snapshotShellJob(job)
		return formatExecuteOutput(output, exitCode), nil
	case <-time.After(timeout):
		return fmt.Sprintf("Command is still running in background. task_id=%s", job.ID), nil
	}
}

func startShellJob(command, workingDir string) (*shellJob, <-chan struct{}, error) {
	cmd := exec.Command("bash", "-lc", command)
	cmd.Dir = workingDir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	job := addShellJob(command, workingDir, cmd)
	done := make(chan struct{})
	go copyShellOutput(job, stdout)
	go copyShellOutput(job, stderr)
	go func() {
		err := cmd.Wait()
		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				appendShellOutput(job, []byte(err.Error()))
				exitCode = 1
			}
		}
		finishShellJob(job, exitCode)
		close(done)
	}()
	return job, done, nil
}

func copyShellOutput(job *shellJob, reader io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			appendShellOutput(job, buf[:n])
		}
		if err != nil {
			return
		}
	}
}
