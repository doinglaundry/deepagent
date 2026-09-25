package graph

import (
	"context"
	"fmt"

	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/schema"
)

func (a *DeepAgent) finishNode(ctx context.Context, s *types.RunState) (*schema.Message, error) {
	s.Pending = nil
	history := a.conversation.History(ctx)
	if len(history) == 0 {
		return nil, fmt.Errorf("agent produced no message")
	}
	message := history[len(history)-1]
	if message.Role == schema.Tool {
		message = CopyMessage(message)
		message.Role = schema.Assistant
		message.ToolCallID = ""
		if message.Content == "" {
			for _, part := range message.UserInputMultiContent {
				if part.Type == schema.ChatMessagePartTypeText {
					message.Content += part.Text
				}
			}
		}
		if a.chunk != nil {
			err := a.chunk(ctx, message)
			if err != nil {
				return nil, err
			}
		}
	}
	return message, nil
}
