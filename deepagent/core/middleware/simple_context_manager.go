package middleware

import (
	"context"

	"eino-cli/deepagent/core/graph"
	"eino-cli/deepagent/core/hook"
	"eino-cli/deepagent/core/types"
	"eino-cli/deepagent/core/utils"
	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
)

// SimpleContextManager keeps the messages visible to the current model run.
// It is intentionally lightweight; durable history and compaction belong to
// agentthread.MemoryContextManager.
type SimpleContextManager struct {
	BaseMiddleware
	history []*schema.Message
}

func NewSimpleContextManager() *SimpleContextManager {
	return &SimpleContextManager{history: make([]*schema.Message, 0)}
}

func (m *SimpleContextManager) Name() string { return "simple_context_manager" }

func (m *SimpleContextManager) BuildStateHandler() types.RunTimeStateful { return m }

func (m *SimpleContextManager) MarshalRuntimeState() string {
	data, _ := sonic.MarshalString(m.history)
	return data
}

func (m *SimpleContextManager) UnmarshalRuntimeState(state string) error {
	return sonic.UnmarshalString(state, &m.history)
}

func (m *SimpleContextManager) Hooks() hook.Hooks {
	return hook.Hooks{
		BeforeModel: m.ModifyModelRequest,
		AfterModel:  m.AfterModel,
	}
}

func (m *SimpleContextManager) AfterModel(ctx context.Context, output hook.ModelOutput, state *types.GraphState) (hook.ModelOutput, error) {
	var err error
	if output.IsStream {
		output.Stream, err = m.ModifyModelStreamResponse(ctx, output.Stream, state)
	} else {
		output.Message, err = m.ModifyModelResponse(ctx, output.Message, state)
	}
	return output, err
}

func (m *SimpleContextManager) ModifyModelRequest(
	ctx context.Context,
	initialContext []*schema.Message,
	messages []*schema.Message,
	state *types.GraphState,
) ([]*schema.Message, error) {
	_ = ctx
	_ = state
	m.history = append(m.history, messages...)
	modelRequest := make([]*schema.Message, 0, len(initialContext)+len(m.history))
	modelRequest = append(modelRequest, initialContext...)
	modelRequest = append(modelRequest, m.history...)
	return modelRequest, nil
}

func (m *SimpleContextManager) ModifyModelResponse(
	ctx context.Context,
	response *schema.Message,
	state *types.GraphState,
) (*schema.Message, error) {
	_ = ctx
	_ = state
	if response == nil {
		return nil, nil
	}
	m.history = append(m.history, response)
	return response, nil
}

func (m *SimpleContextManager) ModifyModelStreamResponse(
	ctx context.Context,
	modelResponse *schema.StreamReader[*schema.Message],
	state *types.GraphState,
) (*schema.StreamReader[*schema.Message], error) {
	_ = state
	if modelResponse == nil {
		return modelResponse, nil
	}
	outputReader, outputWriter := schema.Pipe[*schema.Message](1000)
	go func() {
		defer modelResponse.Close()
		defer outputWriter.Close()
		defer utils.PanicGuard(ctx)
		merger := graph.NewStreamMessageMerger(func(ctx context.Context, chunk *schema.Message) {
			outputWriter.Send(chunk, nil)
		})
		fullMessage, err := merger.Merge(ctx, modelResponse)
		if err != nil {
			outputWriter.Send(nil, err)
			return
		}
		if fullMessage == nil {
			return
		}
		m.history = append(m.history, fullMessage)
	}()
	return outputReader, nil
}
