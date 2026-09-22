package agentthread

import (
	"context"
	"log/slog"

	"eino-cli/deepagent/core/graph"
	"eino-cli/deepagent/core/middlewares"
	"eino-cli/deepagent/core/middlewares/patchtoolcalls"
	"eino-cli/deepagent/core/types"
	"eino-cli/deepagent/utils"

	"github.com/cloudwego/eino/schema"
)

// ctxMngMiddleware adapts the thread ContextManager to DeepAgent's model
// middleware callbacks. It is the only place that combines graph inputs,
// pending thread inputs, history persistence, and pre-sampling compaction.
type ctxMngMiddleware struct {
	middleware.BaseMiddleware
	core    ContextManager
	runID   string
	drainer pendingInputDrainer
	emit    func(context.Context, EventType, any)
}

type pendingInputDrainer interface {
	DrainInput(ctx context.Context) []*schema.Message
}

func (cm *ctxMngMiddleware) ModifyModelRequest(
	ctx context.Context,
	initialContext []*schema.Message,
	userInputOrToolResult []*schema.Message,
	state *types.GraphState,
) ([]*schema.Message, error) {
	_ = state
	input := cm.collectModelInput(ctx, userInputOrToolResult)
	userInput := isUserInput(input)
	if userInput {
		cm.compactIfNeeded(ctx)
	}
	if err := cm.core.AddHistory(ctx, cm.runID, input...); err != nil {
		slog.ErrorContext(ctx, "[ctxMngMiddleware] add model input to history failed", "error", err)
		return nil, err
	}
	if !userInput {
		// Tool results and other non-user inputs complete the previous assistant
		// message, so append them before compaction to keep tool-call pairs intact.
		cm.compactIfNeeded(ctx)
	}
	history := patchtoolcalls.PatchDanglingToolCalls(cm.core.History(ctx))
	return appendModelContext(initialContext, history), nil
}

func (cm *ctxMngMiddleware) ModifyModelResponse(
	ctx context.Context,
	modelResp *schema.Message,
	state *types.GraphState,
) (*schema.Message, error) {
	_ = state
	if modelResp == nil {
		return nil, nil
	}
	if err := cm.core.AddHistory(ctx, cm.runID, modelResp); err != nil {
		slog.ErrorContext(ctx, "[ctxMngMiddleware] add model response to history failed", "error", err)
		return nil, err
	}
	return modelResp, nil
}

func (cm *ctxMngMiddleware) ModifyModelStreamResponse(
	ctx context.Context,
	modelResp *schema.StreamReader[*schema.Message],
	state *types.GraphState,
) (*schema.StreamReader[*schema.Message], error) {
	_ = state
	if modelResp == nil {
		return nil, nil
	}
	outputReader, outputWriter := schema.Pipe[*schema.Message](1000)
	go func() {
		defer func() {
			utils.PanicGuard(ctx)
			modelResp.Close()
			outputWriter.Close()
		}()
		merger := graph.NewStreamMessageMerger(func(ctx context.Context, chunk *schema.Message) {
			outputWriter.Send(chunk, nil)
		})
		fullMessage, err := merger.Merge(ctx, modelResp)
		if err != nil {
			slog.ErrorContext(ctx, "[ctxMngMiddleware] merge model stream failed", "error", err)
			outputWriter.Send(nil, err)
			return
		}
		if fullMessage == nil {
			return
		}
		if err := cm.core.AddHistory(ctx, cm.runID, fullMessage); err != nil {
			slog.ErrorContext(ctx, "[ctxMngMiddleware] add streamed model response to history failed", "error", err)
			outputWriter.Send(nil, err)
		}
	}()
	return outputReader, nil
}

func (cm *ctxMngMiddleware) collectModelInput(ctx context.Context, graphInput []*schema.Message) []*schema.Message {
	if cm.drainer == nil {
		return graphInput
	}
	drained := cm.drainer.DrainInput(ctx)
	if len(drained) == 0 {
		return graphInput
	}
	input := make([]*schema.Message, 0, len(graphInput)+len(drained))
	input = append(input, graphInput...)
	input = append(input, drained...)
	return input
}

func (cm *ctxMngMiddleware) compactIfNeeded(ctx context.Context) {
	if !cm.core.CompactNeeded(ctx) {
		return
	}
	if cm.emit != nil {
		cm.emit(ctx, EventContextCompactStarted, ContextCompactStartedPayload{
			ContextUsage: cm.core.ContextUsage(),
		})
	}
	payload, err := cm.core.Compact(ctx, cm.runID)
	if err != nil {
		slog.ErrorContext(ctx, "[ctxMngMiddleware] compact before model request failed", "error", err)
		return
	}
	if payload != nil && cm.emit != nil {
		cm.emit(ctx, EventContextCompacted, *payload)
	}
}

func isUserInput(input []*schema.Message) bool {
	if len(input) == 0 {
		return true
	}
	for _, msg := range input {
		if msg != nil && msg.Role != schema.User {
			return false
		}
	}
	return true
}

func appendModelContext(initialContext, history []*schema.Message) []*schema.Message {
	fullContext := make([]*schema.Message, 0, len(initialContext)+len(history))
	fullContext = append(fullContext, initialContext...)
	fullContext = append(fullContext, history...)
	return fullContext
}
