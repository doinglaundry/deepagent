package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"eino-cli/deepagent/graph/types"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type ChildRequest struct {
	Name          string
	Prompt        string
	MaxModelCalls int
}

type ChildRunner interface {
	Run(context.Context, ChildRequest, types.ModelChunkSink) (*schema.Message, error)
}

type taskTool struct {
	runner ChildRunner
	names  []string
}

func NewTaskTool(childRunner ChildRunner, subagentNames ...string) ToolDescriptor {
	return ToolDescriptor{Tool: &taskTool{runner: childRunner, names: append([]string(nil), subagentNames...)}}
}

func (taskTool *taskTool) Info(context.Context) (*schema.ToolInfo, error) {
	description := "Run an independent subagent on a self-contained task and return its result. Omit subagent_type for general-purpose."
	subagentTypeParameter := &schema.ParameterInfo{Type: schema.String, Enum: append([]string(nil), taskTool.names...)}
	if len(taskTool.names) > 0 && !slices.Contains(taskTool.names, "general-purpose") {
		description = fmt.Sprintf("Run an independent subagent on a self-contained task and return its result. subagent_type is required; configured subagents: %s.", strings.Join(taskTool.names, ", "))
		subagentTypeParameter.Required = true
	}
	return &schema.ToolInfo{Name: "task", Desc: description, ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"subagent_type": subagentTypeParameter, "description": {Type: schema.String, Required: true}})}, nil
}

func (taskTool *taskTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	if taskTool.runner == nil {
		return "", fmt.Errorf("child runner is required")
	}
	childRequest, err := taskTool.parseChildRequest(arguments)
	if err != nil {
		return "", err
	}
	childMessage, err := taskTool.runner.Run(ctx, childRequest, nil)
	if err != nil {
		return "", err
	}
	if childMessage == nil {
		return "", fmt.Errorf("child returned no message")
	}
	return childMessage.Content, nil
}

func (taskTool *taskTool) parseChildRequest(arguments string) (ChildRequest, error) {
	var taskArgs struct {
		SubagentType string `json:"subagent_type"`
		Description  string `json:"description"`
		Prompt       string `json:"prompt"`
		Task         string `json:"task"`
	}
	err := json.Unmarshal([]byte(arguments), &taskArgs)
	if err != nil {
		return ChildRequest{}, err
	}
	prompt := taskArgs.Description
	if prompt == "" {
		prompt = taskArgs.Prompt
	}
	if prompt == "" {
		prompt = taskArgs.Task
	}
	if strings.TrimSpace(prompt) == "" {
		return ChildRequest{}, fmt.Errorf("description is required")
	}
	subagentName := taskArgs.SubagentType
	if subagentName == "" {
		if len(taskTool.names) > 0 && !slices.Contains(taskTool.names, "general-purpose") {
			return ChildRequest{}, fmt.Errorf("subagent_type is required; configured subagents: %s", strings.Join(taskTool.names, ", "))
		}
		subagentName = "general-purpose"
	}
	return ChildRequest{Name: subagentName, Prompt: prompt}, nil
}

type streamingTaskTool struct{ *taskTool }

func NewStreamingTaskTool(childRunner ChildRunner, readOnly bool, subagentNames ...string) ToolDescriptor {
	return ToolDescriptor{
		Tool:     &streamingTaskTool{&taskTool{runner: childRunner, names: append([]string(nil), subagentNames...)}},
		ReadOnly: readOnly, ParallelSafe: true,
	}
}

func (streamingTaskTool *streamingTaskTool) StreamableRun(ctx context.Context, arguments string, _ ...tool.Option) (*schema.StreamReader[string], error) {
	childRequest, err := streamingTaskTool.parseChildRequest(arguments)
	if err != nil {
		return nil, err
	}
	if streamingTaskTool.runner == nil {
		return nil, fmt.Errorf("child runner is required")
	}
	ctx, cancelChild := context.WithCancel(ctx)
	streamReader, streamWriter := schema.Pipe[string](0)
	streamReader.SetAutomaticClose()
	stopAutomaticClose := context.AfterFunc(ctx, streamReader.Close)
	outputChunks, childDone := make(chan string), make(chan error, 1)
	go func() {
		defer func() {
			panicValue := recover()
			if panicValue != nil {
				childDone <- &types.InternalError{Err: fmt.Errorf("child runner panicked: %v", panicValue)}
			}
		}()
		emittedContent := false
		childMessage, err := streamingTaskTool.runner.Run(ctx, childRequest, func(ctx context.Context, chunk *schema.Message) error {
			if chunk == nil || chunk.Content == "" {
				return nil
			}
			select {
			case outputChunks <- chunk.Content:
				emittedContent = true
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if err == nil && childMessage == nil {
			err = fmt.Errorf("child returned no message")
		}
		if err == nil && !emittedContent && childMessage.Content != "" {
			select {
			case outputChunks <- childMessage.Content:
			case <-ctx.Done():
				err = ctx.Err()
			}
		}
		childDone <- err
	}()
	go func() {
		defer streamWriter.Close()
		defer cancelChild()
		defer stopAutomaticClose()
		heartbeatTicker := time.NewTicker(50 * time.Millisecond)
		defer heartbeatTicker.Stop()
		for {
			select {
			case chunk := <-outputChunks:
				if streamWriter.Send(chunk, nil) {
					cancelChild()
					<-childDone
					return
				}
			case err := <-childDone:
				if err != nil {
					streamWriter.Send("", err)
				}
				return
			case <-heartbeatTicker.C:
				if streamWriter.Send("", nil) {
					cancelChild()
					<-childDone
					return
				}
			case <-ctx.Done():
				<-childDone
				return
			}
		}
	}()
	return schema.StreamReaderWithConvert(streamReader, func(chunk string) (string, error) {
		if chunk == "" {
			return "", schema.ErrNoValue
		}
		return chunk, nil
	}), nil
}
