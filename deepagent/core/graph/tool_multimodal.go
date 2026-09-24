package graph

import (
	"context"
	"fmt"
	"sync"

	"eino-cli/deepagent/core/types"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// Enhanced tools use the same ledger and policy as text tools. Native Eino
// middleware operates on the structured result before conversion to history.
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
