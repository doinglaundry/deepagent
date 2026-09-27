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

func (e *toolExecutor) invoke(ctx context.Context, call types.ToolCall, resume *types.ResumeAnswer, emit types.ToolChunkSink, options ...einotool.Option) (*types.ToolResult, error) {
	ctx = context.WithValue(ctx, toolCallIDKey{}, call.ID)
	ctx = context.WithValue(ctx, toolExecutorKey{}, e)
	descriptor, _ := e.registry.Lookup(call.Name)
	if enhanced, ok := descriptor.Tool.(einotool.EnhancedStreamableTool); ok {
		return e.invokeEnhancedStream(ctx, call, resume, emit, enhanced, options...)
	}
	if enhanced, ok := descriptor.Tool.(einotool.EnhancedInvokableTool); ok {
		return e.invokeEnhanced(ctx, call, resume, enhanced, options...)
	}
	if streaming, ok := descriptor.Tool.(einotool.StreamableTool); ok {
		return e.invokeStream(ctx, call, resume, emit, streaming, options...)
	}
	descriptor, call, early, err := e.authorize(ctx, call, resume)
	if early != nil || err != nil {
		return early, err
	}
	result := &types.ToolResult{CallID: call.ID, ReturnDirect: descriptor.ReturnDirect}
	invokable, ok := descriptor.Tool.(einotool.InvokableTool)
	if !ok {
		return nil, fmt.Errorf("tool %s has no Eino execution interface", call.Name)
	}
	result.Content, err = invokable.InvokableRun(ctx, call.Arguments, options...)
	return finishToolResult(ctx, result, err)
}
func finishToolResult(ctx context.Context, result *types.ToolResult, err error) (*types.ToolResult, error) {
	if err != nil {
		var internal *types.InternalError
		if errors.As(err, &internal) {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		if _, ok := compose.ExtractInterruptInfo(err); ok {
			return nil, err
		}
		if _, ok := compose.IsInterruptRerunError(err); ok {
			return nil, err
		}
		result.IsError = true
		result.Content = err.Error()
	}
	return result, nil
}

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

func (e *toolExecutor) invokeEnhanced(ctx context.Context, call types.ToolCall, resume *types.ResumeAnswer, tool einotool.EnhancedInvokableTool, options ...einotool.Option) (*types.ToolResult, error) {
	descriptor, _ := e.registry.Lookup(call.Name)
	result := &types.ToolResult{CallID: call.ID, ReturnDirect: descriptor.ReturnDirect}
	var mu sync.Mutex
	var invoked bool
	var arguments string
	var saved *compose.EnhancedInvokableToolOutput
	var savedErr error
	endpoint := compose.EnhancedInvokableToolEndpoint(func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedInvokableToolOutput, error) {
		mu.Lock()
		defer mu.Unlock()
		if input == nil || input.CallID != call.ID || input.Name != call.Name {
			return nil, fmt.Errorf("tool middleware changed call identity")
		}
		if invoked {
			if input.Arguments != arguments {
				return nil, fmt.Errorf("tool middleware changed an already executed call")
			}
			return saved, savedErr
		}
		invoked, arguments = true, input.Arguments
		modified := call
		modified.Arguments = input.Arguments
		_, modified, early, err := e.authorize(ctx, modified, resume)
		if err != nil {
			savedErr = err
			return nil, err
		}
		var output *schema.ToolResult
		if early != nil {
			result = early
			output = &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: early.Content}}}
		} else {
			output, err = tool.InvokableRun(ctx, &schema.ToolArgument{Text: modified.Arguments}, input.CallOptions...)
			if err != nil {
				result, savedErr = finishToolResult(ctx, result, err)
				if savedErr != nil {
					return nil, savedErr
				}
				output = &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: result.Content}}}
			}
		}
		if output == nil {
			savedErr = fmt.Errorf("enhanced tool %s returned nil output", call.Name)
			return nil, savedErr
		}
		saved = &compose.EnhancedInvokableToolOutput{Result: output}
		return saved, nil
	})
	for i := len(e.middlewares) - 1; i >= 0; i-- {
		wrappers := e.middlewares[i].ToolCallMiddlewares()
		for j := len(wrappers) - 1; j >= 0; j-- {
			if wrappers[j].EnhancedInvokable != nil {
				endpoint = wrappers[j].EnhancedInvokable(endpoint)
			}
		}
	}
	output, err := endpoint(ctx, &compose.ToolInput{Name: call.Name, CallID: call.ID, Arguments: call.Arguments, CallOptions: options})
	if savedErr != nil {
		return nil, savedErr
	}
	if err != nil {
		return nil, err
	}
	if output == nil || output.Result == nil {
		return nil, fmt.Errorf("enhanced tool middleware returned nil output")
	}
	return finishEnhancedResult(result, output.Result)
}

func finishEnhancedResult(result *types.ToolResult, output *schema.ToolResult) (*types.ToolResult, error) {
	parts, err := output.ToMessageInputParts()
	if err != nil {
		return nil, err
	}
	result.MultiContent = parts
	result.Content = ""
	for _, part := range parts {
		if part.Type == schema.ChatMessagePartTypeText {
			result.Content += part.Text
		}
	}
	return result, nil
}

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
