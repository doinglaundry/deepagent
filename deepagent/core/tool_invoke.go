package deepagents

import (
	"context"
	"errors"
	"fmt"
	"io"

	"eino-cli/deepagent/core/types"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// Every tool passes policy and checkpoint fencing before interface dispatch.
func (e *toolExecutor) invokeTool(ctx context.Context, state types.ToolCallState, emit types.ToolChunkSink) (result *types.ToolResult, err error) {
	call := state.Call
	// Tool code may panic after producing a side effect.
	// Return a system error so execute can finalize the shared ledger and waiters
	// and cleanup cannot remain blocked on this execution forever.
	defer func() {
		recovered := recover()
		if recovered != nil {
			result = nil
			err = fmt.Errorf("tool %s panicked: %v", call.Name, recovered)
		}
	}()
	if e.onToolStart != nil {
		err = e.onToolStart(ctx, state)
		if err != nil {
			return nil, err
		}
	}

	ctx = context.WithValue(ctx, toolCallIDKey{}, call.ID)
	ctx = context.WithValue(ctx, toolExecutorKey{}, e)
	descriptor, early, err := e.authorize(ctx, call)
	if early != nil || err != nil {
		return early, err
	}
	result = &types.ToolResult{CallID: call.ID, ReturnDirect: descriptor.ReturnDirect}
	switch t := descriptor.Tool.(type) {
	case einotool.EnhancedStreamableTool:
		stream, err := t.StreamableRun(ctx, &schema.ToolArgument{Text: call.Arguments})
		if stream != nil {
			defer stream.Close()
		}
		if err != nil {
			return finishToolResult(ctx, result, err)
		}
		if stream == nil {
			return nil, fmt.Errorf("tool %s returned nil stream", call.Name)
		}
		var chunks []*schema.ToolResult
		for {
			err = ctx.Err()
			if err != nil {
				return nil, err
			}
			chunk, readErr := stream.Recv()
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return finishToolResult(ctx, result, readErr)
			}
			if chunk == nil {
				continue
			}
			converted, convertErr := finishEnhancedResult(&types.ToolResult{}, chunk)
			if convertErr != nil {
				return nil, convertErr
			}
			chunks = append(chunks, chunk)
			if emit != nil && converted.Content != "" {
				err = emit(ctx, call, converted.Content)
				if err != nil {
					return nil, err
				}
			}
		}
		merged, err := schema.ConcatToolResults(chunks)
		if err != nil {
			return nil, err
		}
		return finishEnhancedResult(result, merged)
	case einotool.EnhancedInvokableTool:
		output, err := t.InvokableRun(ctx, &schema.ToolArgument{Text: call.Arguments})
		if err != nil {
			return finishToolResult(ctx, result, err)
		}
		return finishEnhancedResult(result, output)
	case einotool.StreamableTool:
		stream, err := t.StreamableRun(ctx, call.Arguments)
		if stream != nil {
			defer stream.Close()
		}
		if err != nil {
			return finishToolResult(ctx, result, err)
		}
		if stream == nil {
			return nil, fmt.Errorf("tool %s returned nil stream", call.Name)
		}
		for {
			err = ctx.Err()
			if err != nil {
				return nil, err
			}
			chunk, readErr := stream.Recv()
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return finishToolResult(ctx, result, readErr)
			}
			result.Content += chunk
			if emit != nil {
				err = emit(ctx, call, chunk)
				if err != nil {
					return nil, err
				}
			}
		}
		return result, nil
	case einotool.InvokableTool:
		result.Content, err = t.InvokableRun(ctx, call.Arguments)
		return finishToolResult(ctx, result, err)
	default:
		return nil, fmt.Errorf("tool %s has no Eino execution interface", call.Name)
	}
}

func finishToolResult(ctx context.Context, result *types.ToolResult, err error) (*types.ToolResult, error) {
	if err == nil {
		return result, nil
	}
	var internal *types.InternalError
	if errors.As(err, &internal) {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	_, interrupted := compose.ExtractInterruptInfo(err)
	_, rerun := compose.IsInterruptRerunError(err)
	if interrupted || rerun || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	result.IsError = true
	result.Content = err.Error()
	return result, nil
}

func finishEnhancedResult(result *types.ToolResult, output *schema.ToolResult) (*types.ToolResult, error) {
	if output == nil {
		return nil, fmt.Errorf("enhanced tool returned nil output")
	}
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
