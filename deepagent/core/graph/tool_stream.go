package graph

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"eino-cli/deepagent/core/types"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func (e *toolExecutor) invokeStream(ctx context.Context, call types.ToolCall, resume *types.ResumeAnswer, emit types.ToolChunkSink, tool einotool.StreamableTool, options ...einotool.Option) (*types.ToolResult, error) {
	descriptor, _ := e.registry.Lookup(call.Name)
	result := &types.ToolResult{CallID: call.ID, ReturnDirect: descriptor.ReturnDirect}
	var mu sync.Mutex
	var opened bool
	var source *schema.StreamReader[string]
	var authorizationErr, toolErr error
	defer func() {
		if source != nil {
			source.Close()
		}
	}()
	endpoint := compose.StreamableToolEndpoint(func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
		mu.Lock()
		defer mu.Unlock()
		if opened {
			return nil, fmt.Errorf("streaming tool middleware called next more than once")
		}
		opened = true
		if input == nil || input.CallID != call.ID || input.Name != call.Name {
			return nil, fmt.Errorf("tool middleware changed call identity")
		}
		modified := call
		modified.Arguments = input.Arguments
		_, modified, early, err := e.authorize(ctx, modified, resume)
		if err != nil {
			authorizationErr = err
			return nil, err
		}
		if early != nil {
			result = early
			source = schema.StreamReaderFromArray([]string{early.Content})
		} else {
			source, toolErr = tool.StreamableRun(ctx, modified.Arguments, input.CallOptions...)
		}
		if source != nil {
			source.SetAutomaticClose()
		}
		if toolErr != nil {
			return nil, toolErr
		}
		if source == nil {
			return nil, fmt.Errorf("tool %s returned nil stream", call.Name)
		}
		return &compose.StreamToolOutput{Result: source}, nil
	})
	for i := len(e.middlewares) - 1; i >= 0; i-- {
		wrappers := e.middlewares[i].ToolCallMiddlewares()
		for j := len(wrappers) - 1; j >= 0; j-- {
			if wrappers[j].Streamable != nil {
				endpoint = wrappers[j].Streamable(endpoint)
			}
		}
	}
	output, err := endpoint(ctx, &compose.ToolInput{Name: call.Name, CallID: call.ID, Arguments: call.Arguments, CallOptions: options})
	if output != nil && output.Result != nil {
		defer output.Result.Close()
	}
	if authorizationErr != nil {
		return nil, authorizationErr
	}
	if err != nil {
		if toolErr != nil && errors.Is(err, toolErr) {
			return finishToolResult(ctx, result, err)
		}
		return nil, err
	}
	if output == nil || output.Result == nil {
		return nil, fmt.Errorf("streaming tool middleware returned nil output")
	}
	result.Content = ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		chunk, err := output.Result.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return finishToolResult(ctx, result, err)
		}
		result.Content += chunk
		if emit != nil {
			if err := emit(ctx, call, chunk); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}
