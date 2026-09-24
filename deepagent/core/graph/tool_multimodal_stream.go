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

func (e *toolExecutor) invokeEnhancedStream(ctx context.Context, call types.ToolCall, resume *types.ResumeAnswer, emit types.ToolChunkSink, tool einotool.EnhancedStreamableTool, options ...einotool.Option) (*types.ToolResult, error) {
	descriptor, _ := e.registry.Lookup(call.Name)
	result := &types.ToolResult{CallID: call.ID, ReturnDirect: descriptor.ReturnDirect}
	var mu sync.Mutex
	var opened bool
	var source *schema.StreamReader[*schema.ToolResult]
	var authorizationErr, toolErr error
	defer func() {
		if source != nil {
			source.Close()
		}
	}()
	endpoint := compose.EnhancedStreamableToolEndpoint(func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedStreamableToolOutput, error) {
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
			source = schema.StreamReaderFromArray([]*schema.ToolResult{{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: early.Content}}}})
		} else {
			source, toolErr = tool.StreamableRun(ctx, &schema.ToolArgument{Text: modified.Arguments}, input.CallOptions...)
		}
		if source != nil {
			source.SetAutomaticClose()
		}
		if toolErr != nil {
			return nil, toolErr
		}
		if source == nil {
			return nil, fmt.Errorf("enhanced tool %s returned nil stream", call.Name)
		}
		return &compose.EnhancedStreamableToolOutput{Result: source}, nil
	})
	for i := len(e.middlewares) - 1; i >= 0; i-- {
		wrappers := e.middlewares[i].ToolCallMiddlewares()
		for j := len(wrappers) - 1; j >= 0; j-- {
			if wrappers[j].EnhancedStreamable != nil {
				endpoint = wrappers[j].EnhancedStreamable(endpoint)
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
		return nil, fmt.Errorf("enhanced streaming middleware returned nil output")
	}
	var chunks []*schema.ToolResult
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
		if chunk == nil {
			continue
		}
		// Validate each chunk before publishing its text. Eino performs the final
		// multimodal merge, including rejecting conflicting non-text parts.
		converted, err := finishEnhancedResult(&types.ToolResult{}, chunk)
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
		if emit != nil && converted.Content != "" {
			if err := emit(ctx, call, converted.Content); err != nil {
				return nil, err
			}
		}
	}
	merged, err := schema.ConcatToolResults(chunks)
	if err != nil {
		return nil, err
	}
	return finishEnhancedResult(result, merged)
}
