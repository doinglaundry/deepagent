package graph

import (
	"context"
	"encoding/json"
	"fmt"

	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/schema"
)

func toolHookDone(s *types.RunState, key string) bool {
	var call int
	raw := s.Extensions[key]
	return len(raw) > 0 && json.Unmarshal(raw, &call) == nil && call == s.ModelCalls
}
func markToolHook(s *types.RunState, key string) {
	if s.Extensions == nil {
		s.Extensions = map[string]json.RawMessage{}
	}
	s.Extensions[key], _ = json.Marshal(s.ModelCalls)
}
func (a *DeepAgent) prepareToolCalls(ctx context.Context, s *types.RunState) error {
	if a.cfg.ToolNodePreHandler == nil || toolHookDone(s, "tools_pre_model_call") {
		return nil
	}
	history := a.conversation.History(ctx)
	if len(history) == 0 || history[len(history)-1].Role != schema.Assistant {
		return fmt.Errorf("tool pre-handler requires assistant history")
	}
	message, err := a.cfg.ToolNodePreHandler(ctx, CopyMessage(history[len(history)-1]))
	if err != nil {
		return err
	}
	if message == nil || len(message.ToolCalls) != len(s.Calls) {
		return fmt.Errorf("tool pre-handler must preserve call identities")
	}
	byID := make(map[string]types.ToolCallState, len(s.Calls))
	for _, call := range s.Calls {
		byID[call.Call.ID] = call
	}
	calls := make([]types.ToolCallState, 0, len(s.Calls))
	for i, updated := range message.ToolCalls {
		call, ok := byID[updated.ID]
		if !ok {
			return fmt.Errorf("tool pre-handler changed call identity %q", updated.ID)
		}
		delete(byID, updated.ID)
		call.Call.Index, call.Call.Name, call.Call.Arguments = i, updated.Function.Name, updated.Function.Arguments
		calls = append(calls, call)
	}
	s.Calls = calls
	markToolHook(s, "tools_pre_model_call")
	return nil
}
func (a *DeepAgent) finishToolCalls(ctx context.Context, s *types.RunState, results []types.ToolResult) ([]*schema.Message, error) {
	messages := make([]*schema.Message, len(results))
	for i, result := range results {
		messages[i] = schema.ToolMessage(result.Content, result.CallID)
		if len(result.MultiContent) > 0 {
			messages[i].Content = ""
			messages[i].UserInputMultiContent = result.MultiContent
		}
	}
	if a.cfg.ToolNodePostHandler == nil {
		return messages, nil
	}
	if toolHookDone(s, "tools_post_model_call") {
		err := json.Unmarshal(s.Extensions["tools_post_messages"], &messages)
		if err != nil {
			return nil, err
		}
	} else {
		var err error
		messages, err = a.cfg.ToolNodePostHandler(ctx, messages)
		if err != nil {
			return nil, err
		}
	}
	if len(messages) != len(results) {
		return nil, fmt.Errorf("tool post-handler must preserve call results")
	}
	byID := make(map[string]*schema.Message, len(messages))
	for _, message := range messages {
		if message == nil || message.Role != schema.Tool || byID[message.ToolCallID] != nil {
			return nil, fmt.Errorf("tool post-handler returned invalid tool message")
		}
		byID[message.ToolCallID] = message
	}
	ordered := make([]*schema.Message, len(results))
	for i := range results {
		message := byID[results[i].CallID]
		if message == nil {
			return nil, fmt.Errorf("tool post-handler changed call identity %q", results[i].CallID)
		}
		ordered[i] = message
		results[i].Content = message.Content
		results[i].MultiContent = message.UserInputMultiContent
	}
	raw, err := json.Marshal(ordered)
	if err != nil {
		return nil, err
	}
	markToolHook(s, "tools_post_model_call")
	s.Extensions["tools_post_messages"] = raw
	return ordered, nil
}
