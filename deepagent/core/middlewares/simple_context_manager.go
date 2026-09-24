package middleware

import (
	"context"

	"eino-cli/deepagent/core/graph"
	"eino-cli/deepagent/core/hooks"
	"eino-cli/deepagent/core/internal/conversation"
	"eino-cli/deepagent/core/types"
	"fmt"
	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
	"time"
)

// SimpleContextManager preserves the legacy hook API over the canonical
// in-memory Conversation. Graph uses it directly as its Conversation owner.
type SimpleContextManager struct {
	BaseMiddleware
	*conversation.Conversation
}

func NewSimpleContextManager() *SimpleContextManager {
	return &SimpleContextManager{Conversation: conversation.New("", nil, nil, nil)}
}

func (m *SimpleContextManager) Name() string { return "simple_context_manager" }

func (m *SimpleContextManager) BuildStateHandler() types.RunTimeStateful { return m }

func (m *SimpleContextManager) MarshalRuntimeState() string {
	data, _ := sonic.MarshalString(m.History(context.Background()))
	return data
}

func (m *SimpleContextManager) UnmarshalRuntimeState(state string) error {
	var messages []*schema.Message
	if err := sonic.UnmarshalString(state, &messages); err != nil {
		return err
	}
	restored := conversation.New("", nil, nil, nil)
	if err := restored.AddHistory(context.Background(), "", messages...); err != nil {
		return err
	}
	m.Conversation = restored
	return nil
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
	if types.RunStateFromContext(ctx) != nil {
		return messages, nil // Canonical Conversation already owns the complete request history.
	}
	_ = ctx
	_ = state
	if err := m.AddHistory(ctx, "", messages...); err != nil {
		return nil, err
	}
	return m.BuildRequest(ctx, initialContext)
}

func (m *SimpleContextManager) ModifyModelResponse(
	ctx context.Context,
	response *schema.Message,
	state *types.GraphState,
) (*schema.Message, error) {
	if types.RunStateFromContext(ctx) != nil {
		return response, nil
	}
	_ = ctx
	_ = state
	if response == nil {
		return nil, nil
	}
	return response, m.AddHistory(ctx, "", response)
}

func (m *SimpleContextManager) ModifyModelStreamResponse(
	ctx context.Context,
	modelResponse *schema.StreamReader[*schema.Message],
	state *types.GraphState,
) (*schema.StreamReader[*schema.Message], error) {
	if types.RunStateFromContext(ctx) != nil {
		return modelResponse, nil
	}
	_ = state
	if modelResponse == nil {
		return modelResponse, nil
	}
	streamCtx, cancel := context.WithCancel(ctx)
	outputReader, outputWriter := schema.Pipe[*schema.Message](0)
	outputReader.SetAutomaticClose()
	modelResponse.SetAutomaticClose()
	stopClose := context.AfterFunc(streamCtx, func() { outputReader.Close(); modelResponse.Close() })
	chunks := make(chan *schema.Message)
	done := make(chan error, 1)
	go func() {
		var mergeErr error
		defer func() {
			if recovered := recover(); recovered != nil {
				mergeErr = fmt.Errorf("merge model stream: %v", recovered)
			}
			modelResponse.Close()
			done <- mergeErr
		}()
		merger := graph.NewStreamMessageMerger(func(ctx context.Context, chunk *schema.Message) {
			select {
			case chunks <- chunk:
			case <-ctx.Done():
			}
		})
		var message *schema.Message
		message, mergeErr = merger.Merge(streamCtx, modelResponse)
		if mergeErr == nil && message != nil {
			mergeErr = m.AddHistory(streamCtx, "", message)
		}
	}()
	go func() {
		defer outputWriter.Close()
		defer cancel()
		defer stopClose()
		// Eino exposes consumer closure through Send only. Private nil probes
		// detect closure while the provider is silent and are filtered below.
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case chunk := <-chunks:
				if outputWriter.Send(chunk, nil) {
					cancel()
					<-done
					return
				}
			case err := <-done:
				if err != nil {
					outputWriter.Send(nil, err)
				}
				return
			case <-ticker.C:
				if outputWriter.Send(nil, nil) {
					cancel()
					<-done
					return
				}
			case <-streamCtx.Done():
				<-done
				return
			}
		}
	}()
	return schema.StreamReaderWithConvert(outputReader, func(message *schema.Message) (*schema.Message, error) {
		if message == nil {
			return nil, schema.ErrNoValue
		}
		return message, nil
	}), nil
}

var _ graph.Conversation = (*SimpleContextManager)(nil)
