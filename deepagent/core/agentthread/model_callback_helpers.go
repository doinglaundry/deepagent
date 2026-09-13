package agentthread

import (
	"context"
	agentgraph "eino-cli/deepagent/core/graph"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// llmEndFromCallbackOutput snapshots a callback result before exposing it as
// an agentthread event, preventing later callback mutation from changing the
// event payload.
func llmEndFromCallbackOutput(output *model.CallbackOutput, llmResponseID string) LLMEnd {
	if output == nil {
		return LLMEnd{LLMResponseID: llmResponseID}
	}
	callbackOutput := model.CallbackOutput{
		Config:     cloneModelConfig(output.Config),
		TokenUsage: cloneModelTokenUsage(output.TokenUsage),
		Extra:      cloneAnyMap(output.Extra),
	}
	if output.Message != nil {
		callbackOutput.Message = agentgraph.CopyMessage(output.Message)
	}
	return LLMEnd{CallbackOutput: callbackOutput, LLMResponseID: llmResponseID}
}

type modelCallbackStreamAccumulator struct {
	config     *model.Config
	tokenUsage *model.TokenUsage
	extra      map[string]any
	lastMsg    *schema.Message
}

func (a *modelCallbackStreamAccumulator) observe(output *model.CallbackOutput) {
	if output == nil {
		return
	}
	if output.Config != nil {
		a.config = cloneModelConfig(output.Config)
	}
	if output.Extra != nil {
		a.extra = cloneAnyMap(output.Extra)
	}
	if output.Message != nil {
		a.lastMsg = agentgraph.CopyMessage(output.Message)
	}
	if output.TokenUsage != nil {
		a.tokenUsage = cloneModelTokenUsage(output.TokenUsage)
	}
}
func (a *modelCallbackStreamAccumulator) applyLastMessageFields(merged *schema.Message) {
	if a == nil || merged == nil || a.lastMsg == nil {
		return
	}
	if merged.Role == "" && a.lastMsg.Role != "" {
		merged.Role = a.lastMsg.Role
	}
	if a.lastMsg.ResponseMeta != nil {
		merged.ResponseMeta = a.lastMsg.ResponseMeta
	}
	if len(a.lastMsg.Extra) > 0 {
		if merged.Extra == nil {
			merged.Extra = map[string]any{}
		}
		for k, v := range a.lastMsg.Extra {
			merged.Extra[k] = v
		}
	}
}
func mergeModelCallbackStream(ctx context.Context, output *schema.StreamReader[*model.CallbackOutput], llmResponseID string, onChunk func(context.Context, *schema.Message)) (LLMEnd, int, error) {
	acc := &modelCallbackStreamAccumulator{}
	chunkCount := 0
	messageStream := schema.StreamReaderWithConvert(output, func(chunk *model.CallbackOutput) (*schema.Message, error) {
		acc.observe(chunk)
		if chunk == nil || chunk.Message == nil {
			return nil, schema.ErrNoValue
		}
		if isEmptyModelMessageChunk(chunk.Message) {
			return nil, schema.ErrNoValue
		}
		return chunk.Message, nil
	})
	defer messageStream.Close()
	merger := agentgraph.NewStreamMessageMerger(func(ctx context.Context, chunk *schema.Message) {
		if chunk == nil {
			return
		}
		if chunk.Content != "" {
			chunkCount++
		}
		if onChunk != nil {
			onChunk(ctx, chunk)
		}
	})
	merged, err := merger.Merge(ctx, messageStream)
	if err != nil {
		return LLMEnd{}, chunkCount, err
	}
	if merged != nil {
		acc.applyLastMessageFields(merged)
	}
	return LLMEnd{
		CallbackOutput: model.CallbackOutput{
			Message:    merged,
			Config:     cloneModelConfig(acc.config),
			TokenUsage: cloneModelTokenUsage(acc.tokenUsage),
			Extra:      cloneAnyMap(acc.extra),
		},
		LLMResponseID: llmResponseID,
	}, chunkCount, nil
}
func isEmptyModelMessageChunk(msg *schema.Message) bool {
	if msg == nil {
		return true
	}
	return msg.Role == "" && msg.Content == "" && msg.ReasoningContent == "" &&
		msg.Name == "" && msg.ToolCallID == "" && msg.ToolName == "" &&
		len(msg.ToolCalls) == 0 && len(msg.MultiContent) == 0 &&
		len(msg.UserInputMultiContent) == 0 && len(msg.AssistantGenMultiContent) == 0
}
func cloneModelTokenUsage(usage *model.TokenUsage) *model.TokenUsage {
	if usage == nil {
		return nil
	}
	copied := *usage
	return &copied
}
func cloneModelConfig(cfg *model.Config) *model.Config {
	if cfg == nil {
		return nil
	}
	copied := *cfg
	if cfg.Stop != nil {
		copied.Stop = append([]string(nil), cfg.Stop...)
	}
	return &copied
}
func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
