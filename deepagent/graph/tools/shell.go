package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	filesystempkg "eino-cli/deepagent/graph/filesystem"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type commandTool struct {
	commandService filesystempkg.CommandService
	toolName       string
	defaultTimeout time.Duration
}

func NewCommandTools(commandService filesystempkg.CommandService, defaultTimeouts ...time.Duration) []ToolDescriptor {
	timeout := 30 * time.Second
	if len(defaultTimeouts) > 0 && defaultTimeouts[0] > 0 {
		timeout = defaultTimeouts[0]
	}
	return []ToolDescriptor{
		{Tool: &executeTool{commandTool{commandService: commandService, toolName: "execute", defaultTimeout: timeout}}, RequiresApproval: true},
		{Tool: &commandTool{commandService: commandService, toolName: "shell", defaultTimeout: timeout}, RequiresApproval: true},
		{Tool: &commandTool{commandService: commandService, toolName: "await_shell", defaultTimeout: timeout}, ReadOnly: true},
	}
}

func (commandTool *commandTool) Info(context.Context) (*schema.ToolInfo, error) {
	parameters := map[string]*schema.ParameterInfo{"command": {Type: schema.String, Required: true}, "working_directory": {Type: schema.String}, "timeout_ms": {Type: schema.Integer}}
	description := "Execute a command in the thread workspace."
	if commandTool.toolName == "execute" {
		parameters["timeout_seconds"] = &schema.ParameterInfo{Type: schema.Integer, Desc: "Timeout in seconds, maximum 300; timeout_ms takes precedence."}
	}
	if commandTool.toolName == "shell" {
		description = "Start a command; return a task_id when the foreground wait expires."
	}
	if commandTool.toolName == "await_shell" {
		description = "Wait for this thread's command to finish or produce a matching output pattern."
		parameters = map[string]*schema.ParameterInfo{"task_id": {Type: schema.String, Required: true}, "pattern": {Type: schema.String}, "timeout_ms": {Type: schema.Integer}, "since_offset": {Type: schema.Integer}}
	}
	return &schema.ToolInfo{Name: commandTool.toolName, Desc: description, ParamsOneOf: schema.NewParamsOneOfByParams(parameters)}, nil
}

func (commandTool *commandTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	if commandTool.commandService == nil {
		return "", fmt.Errorf("command service is required")
	}
	var commandArgs struct {
		Command        string `json:"command"`
		WorkDir        string `json:"working_directory"`
		TimeoutMS      int    `json:"timeout_ms"`
		TimeoutSeconds int    `json:"timeout_seconds"`
		TaskID         string `json:"task_id"`
		Pattern        string `json:"pattern"`
		SinceOffset    int    `json:"since_offset"`
	}
	decodeErr := json.Unmarshal([]byte(arguments), &commandArgs)
	if decodeErr != nil {
		return "", decodeErr
	}
	timeout := time.Duration(commandArgs.TimeoutMS) * time.Millisecond
	if timeout <= 0 && commandArgs.TimeoutSeconds > 0 {
		timeout = time.Duration(commandArgs.TimeoutSeconds) * time.Second
	}
	if timeout <= 0 {
		timeout = commandTool.defaultTimeout
	}

	taskID := commandArgs.TaskID
	if commandTool.toolName == "shell" {
		var err error
		taskID, err = commandTool.commandService.Start(context.WithoutCancel(ctx), filesystempkg.CommandRequest{Command: commandArgs.Command, WorkDir: commandArgs.WorkDir, MaxOutputBytes: 64 << 10})
		if err != nil {
			return "", err
		}
	}
	if strings.TrimSpace(taskID) == "" {
		return "", fmt.Errorf("task_id is required")
	}
	waitCtx, cancelWait := context.WithTimeout(ctx, timeout)
	defer cancelWait()
	commandSnapshot, err := commandTool.commandService.Wait(waitCtx, taskID, commandArgs.Pattern, commandArgs.SinceOffset)
	if commandTool.toolName == "shell" && ctx.Err() != nil {
		_ = commandTool.commandService.Cancel(context.Background(), taskID)
		return "", ctx.Err()
	}
	if err != nil && (!errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil) {
		return "", err
	}
	if commandSnapshot == nil {
		return "", fmt.Errorf("command returned no snapshot")
	}
	status := "running"
	if commandSnapshot.Done {
		status = "done"
	}
	return fmt.Sprintf("task_id=%s status=%s exit_code=%d output_offset=%d\n%s", commandSnapshot.ID, status, commandSnapshot.ExitCode, commandSnapshot.Offset, commandSnapshot.Output), nil
}
