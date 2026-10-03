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
	service        filesystempkg.CommandService
	name           string
	defaultTimeout time.Duration
}

func NewCommandTools(service filesystempkg.CommandService, defaults ...time.Duration) []ToolDescriptor {
	timeout := 30 * time.Second
	if len(defaults) > 0 && defaults[0] > 0 {
		timeout = defaults[0]
	}
	return []ToolDescriptor{
		{Tool: &executeTool{commandTool{service: service, name: "execute", defaultTimeout: timeout}}, RequiresApproval: true},
		{Tool: &commandTool{service: service, name: "shell", defaultTimeout: timeout}, RequiresApproval: true},
		{Tool: &commandTool{service: service, name: "await_shell", defaultTimeout: timeout}, ReadOnly: true},
	}
}

func (t *commandTool) Info(context.Context) (*schema.ToolInfo, error) {
	params := map[string]*schema.ParameterInfo{"command": {Type: schema.String, Required: true}, "working_directory": {Type: schema.String}, "timeout_ms": {Type: schema.Integer}}
	desc := "Execute a command in the thread workspace."
	if t.name == "execute" {
		params["timeout_seconds"] = &schema.ParameterInfo{Type: schema.Integer, Desc: "Timeout in seconds, maximum 300; timeout_ms takes precedence."}
	}
	if t.name == "shell" {
		desc = "Start a command; return a task_id when the foreground wait expires."
	}
	if t.name == "await_shell" {
		desc = "Wait for this thread's command to finish or produce a matching output pattern."
		params = map[string]*schema.ParameterInfo{"task_id": {Type: schema.String, Required: true}, "pattern": {Type: schema.String}, "timeout_ms": {Type: schema.Integer}, "since_offset": {Type: schema.Integer}}
	}
	return &schema.ToolInfo{Name: t.name, Desc: desc, ParamsOneOf: schema.NewParamsOneOfByParams(params)}, nil
}

func (t *commandTool) InvokableRun(ctx context.Context, raw string, _ ...tool.Option) (string, error) {
	if t.service == nil {
		return "", fmt.Errorf("command service is required")
	}
	var input struct {
		Command        string `json:"command"`
		WorkDir        string `json:"working_directory"`
		TimeoutMS      int    `json:"timeout_ms"`
		TimeoutSeconds int    `json:"timeout_seconds"`
		TaskID         string `json:"task_id"`
		Pattern        string `json:"pattern"`
		SinceOffset    int    `json:"since_offset"`
	}
	decodeErr := json.Unmarshal([]byte(raw), &input)
	if decodeErr != nil {
		return "", decodeErr
	}
	timeout := time.Duration(input.TimeoutMS) * time.Millisecond
	if timeout <= 0 && input.TimeoutSeconds > 0 {
		timeout = time.Duration(input.TimeoutSeconds) * time.Second
	}
	if timeout <= 0 {
		timeout = t.defaultTimeout
	}

	id := input.TaskID
	if t.name == "shell" {
		var err error
		id, err = t.service.Start(context.WithoutCancel(ctx), filesystempkg.CommandRequest{Command: input.Command, WorkDir: input.WorkDir, MaxOutputBytes: 64 << 10})
		if err != nil {
			return "", err
		}
	}
	if strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("task_id is required")
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	snapshot, err := t.service.Wait(waitCtx, id, input.Pattern, input.SinceOffset)
	if t.name == "shell" && ctx.Err() != nil {
		_ = t.service.Cancel(context.Background(), id)
		return "", ctx.Err()
	}
	if err != nil && (!errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil) {
		return "", err
	}
	if snapshot == nil {
		return "", fmt.Errorf("command returned no snapshot")
	}
	status := "running"
	if snapshot.Done {
		status = "done"
	}
	return fmt.Sprintf("task_id=%s status=%s exit_code=%d output_offset=%d\n%s", snapshot.ID, status, snapshot.ExitCode, snapshot.Offset, snapshot.Output), nil
}
