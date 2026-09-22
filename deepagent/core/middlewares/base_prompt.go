package middleware

import (
	"context"

	"github.com/cloudwego/eino/schema"
)

const BasePromptMiddlewareName = "base_prompt"

// BasePromptMiddleware supplies the initial system context for a model run.
type BasePromptMiddleware struct {
	BaseMiddleware
	prompt string
}

func NewBasePromptMiddleware(prompt string) *BasePromptMiddleware {
	return &BasePromptMiddleware{prompt: prompt}
}

func (m *BasePromptMiddleware) Name() string { return BasePromptMiddlewareName }

func (m *BasePromptMiddleware) BuildInitialContext(context.Context) ([]*schema.Message, error) {
	if m == nil || m.prompt == "" {
		return nil, nil
	}
	return []*schema.Message{schema.SystemMessage(m.prompt)}, nil
}

func (m *BasePromptMiddleware) BuildPrompt(ctx context.Context) ([]*schema.Message, error) {
	return m.BuildInitialContext(ctx)
}
