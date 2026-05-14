package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

const awaitShellToolDesc = `Wait for a background shell task to finish or for a regex pattern to appear in its output. Returns current status and output.`

type awaitShellArgs struct {
	TaskID      string `json:"task_id" jsonschema:"required,description=Background shell task id"`
	TimeoutMS   int    `json:"timeout_ms,omitempty" jsonschema:"description=Maximum wait time in milliseconds"`
	Pattern     string `json:"pattern,omitempty" jsonschema:"description=Optional regex to wait for in output"`
	SinceOffset int    `json:"since_offset,omitempty" jsonschema:"description=Return output starting at byte offset"`
}

func GetAwaitShellTool(root string) (tool.BaseTool, error) {
	return utils.InferTool("await_shell", awaitShellToolDesc,
		func(ctx context.Context, in awaitShellArgs) (string, error) {
			return awaitShell(in)
		})
}

func awaitShell(in awaitShellArgs) (string, error) {
	job, ok := getShellJob(in.TaskID)
	if !ok {
		return "", fmt.Errorf("unknown task_id: %s", in.TaskID)
	}
	timeout := time.Duration(in.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	var re *regexp.Regexp
	if strings.TrimSpace(in.Pattern) != "" {
		compiled, err := regexp.Compile(in.Pattern)
		if err != nil {
			return "", err
		}
		re = compiled
	}
	deadline := time.After(timeout)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		output, done, exitCode := snapshotShellJob(job)
		if re != nil && re.MatchString(output) {
			return formatShellSnapshot(job.ID, output, done, exitCode, in.SinceOffset), nil
		}
		if re == nil && done {
			return formatShellSnapshot(job.ID, output, true, exitCode, in.SinceOffset), nil
		}
		if re != nil && done {
			return formatShellSnapshot(job.ID, output, true, exitCode, in.SinceOffset), nil
		}
		select {
		case <-deadline:
			return formatShellSnapshot(job.ID, output, false, exitCode, in.SinceOffset), nil
		case <-ticker.C:
		}
	}
}

func formatShellSnapshot(taskID, output string, done bool, exitCode, sinceOffset int) string {
	if sinceOffset < 0 {
		sinceOffset = 0
	}
	if sinceOffset > len(output) {
		sinceOffset = len(output)
	}
	status := "running"
	if done {
		status = fmt.Sprintf("done exit_code=%d", exitCode)
	}
	body := output[sinceOffset:]
	if strings.TrimSpace(body) == "" {
		body = "[no output]"
	}
	return fmt.Sprintf("task_id=%s status=%s output_offset=%d\n%s", taskID, status, len(output), truncateToolOutput(body, 64*1024))
}
