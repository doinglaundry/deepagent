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

func NewTaskTool(runner ChildRunner, names ...string) ToolDescriptor {
	return ToolDescriptor{Tool: &taskTool{runner: runner, names: append([]string(nil), names...)}}
}

func (t *taskTool) Info(context.Context) (*schema.ToolInfo, error) {
	description := "Run an independent subagent on a self-contained task and return its result. Omit subagent_type for general-purpose."
	subagentType := &schema.ParameterInfo{Type: schema.String, Enum: append([]string(nil), t.names...)}
	if len(t.names) > 0 && !slices.Contains(t.names, "general-purpose") {
		description = fmt.Sprintf("Run an independent subagent on a self-contained task and return its result. subagent_type is required; configured subagents: %s.", strings.Join(t.names, ", "))
		subagentType.Required = true
	}
	return &schema.ToolInfo{Name: "task", Desc: description, ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"subagent_type": subagentType, "description": {Type: schema.String, Required: true}})}, nil
}

func (t *taskTool) InvokableRun(ctx context.Context, raw string, _ ...tool.Option) (string, error) {
	if t.runner == nil {
		return "", fmt.Errorf("child runner is required")
	}
	request, err := t.parseChildRequest(raw)
	if err != nil {
		return "", err
	}
	message, err := t.runner.Run(ctx, request, nil)
	if err != nil {
		return "", err
	}
	if message == nil {
		return "", fmt.Errorf("child returned no message")
	}
	return message.Content, nil
}

func (t *taskTool) parseChildRequest(raw string) (ChildRequest, error) {
	var input struct {
		SubagentType string `json:"subagent_type"`
		Description  string `json:"description"`
		Prompt       string `json:"prompt"`
		Task         string `json:"task"`
	}
	err := json.Unmarshal([]byte(raw), &input)
	if err != nil {
		return ChildRequest{}, err
	}
	prompt := input.Description
	if prompt == "" {
		prompt = input.Prompt
	}
	if prompt == "" {
		prompt = input.Task
	}
	if strings.TrimSpace(prompt) == "" {
		return ChildRequest{}, fmt.Errorf("description is required")
	}
	name := input.SubagentType
	if name == "" {
		if len(t.names) > 0 && !slices.Contains(t.names, "general-purpose") {
			return ChildRequest{}, fmt.Errorf("subagent_type is required; configured subagents: %s", strings.Join(t.names, ", "))
		}
		name = "general-purpose"
	}
	return ChildRequest{Name: name, Prompt: prompt}, nil
}

type streamingTaskTool struct{ *taskTool }

func NewStreamingTaskTool(runner ChildRunner, readOnly bool, names ...string) ToolDescriptor {
	return ToolDescriptor{
		Tool:     &streamingTaskTool{&taskTool{runner: runner, names: append([]string(nil), names...)}},
		ReadOnly: readOnly, ParallelSafe: true,
	}
}

func (t *streamingTaskTool) StreamableRun(ctx context.Context, raw string, _ ...tool.Option) (*schema.StreamReader[string], error) {
	request, err := t.parseChildRequest(raw)
	if err != nil {
		return nil, err
	}
	if t.runner == nil {
		return nil, fmt.Errorf("child runner is required")
	}
	ctx, cancel := context.WithCancel(ctx)
	reader, writer := schema.Pipe[string](0)
	reader.SetAutomaticClose()
	stopClose := context.AfterFunc(ctx, reader.Close)
	chunks, done := make(chan string), make(chan error, 1)
	go func() {
		defer func() {
			recovered := recover()
			if recovered != nil {
				done <- &types.InternalError{Err: fmt.Errorf("child runner panicked: %v", recovered)}
			}
		}()
		emitted := false
		message, err := t.runner.Run(ctx, request, func(ctx context.Context, chunk *schema.Message) error {
			if chunk == nil || chunk.Content == "" {
				return nil
			}
			select {
			case chunks <- chunk.Content:
				emitted = true
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if err == nil && message == nil {
			err = fmt.Errorf("child returned no message")
		}
		if err == nil && !emitted && message.Content != "" {
			select {
			case chunks <- message.Content:
			case <-ctx.Done():
				err = ctx.Err()
			}
		}
		done <- err
	}()
	go func() {
		defer writer.Close()
		defer cancel()
		defer stopClose()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case chunk := <-chunks:
				if writer.Send(chunk, nil) {
					cancel()
					<-done
					return
				}
			case err := <-done:
				if err != nil {
					writer.Send("", err)
				}
				return
			case <-ticker.C:
				if writer.Send("", nil) {
					cancel()
					<-done
					return
				}
			case <-ctx.Done():
				<-done
				return
			}
		}
	}()
	return schema.StreamReaderWithConvert(reader, func(chunk string) (string, error) {
		if chunk == "" {
			return "", schema.ErrNoValue
		}
		return chunk, nil
	}), nil
}
