package eino

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type ModelConfig struct {
	Name string
}

func getModelConfig(runtimeModel string) (ModelConfig, error) {
	name := strings.TrimSpace(runtimeModel)
	if name == "" {
		return ModelConfig{}, fmt.Errorf("runtime model is required")
	}
	return ModelConfig{Name: name}, nil
}

func buildBaseChatModel(_ context.Context, cfg ModelConfig) (model.BaseChatModel, error) {
	return stubChatModel{modelName: cfg.Name}, nil
}

type stubChatModel struct {
	modelName string
}

func (s stubChatModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("deep runtime response from "+s.modelName+": "+lastMessageContent(input), nil), nil
}

func (s stubChatModel) Stream(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := s.Generate(ctx, input)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func lastMessageContent(messages []*schema.Message) string {
	if len(messages) == 0 {
		return ""
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if msg := messages[i]; msg != nil && strings.TrimSpace(msg.Content) != "" {
			return strings.TrimSpace(msg.Content)
		}
	}
	return ""
}
