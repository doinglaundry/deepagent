package agent

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type deepCompatTestModel struct{}

func (m deepCompatTestModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	return schema.AssistantMessage("ok", nil), nil
}

func (m deepCompatTestModel) Stream(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok", nil)}), nil
}

type namedTestTool struct {
	name string
}

func (t namedTestTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name}, nil
}

func TestBuildDeepCompatibleHandlersOrdersBuiltinsBeforeUserHandlers(t *testing.T) {
	ctx := context.Background()
	userTool := namedTestTool{name: "user_tool"}

	handlers, err := buildDeepCompatibleHandlers(ctx, deepCompatibleAgentConfig{
		ChatModel:         deepCompatTestModel{},
		Instruction:       "test instruction",
		MaxIterations:     2,
		EnableWriteTodos:  true,
		EnableGeneralTask: true,
		Handlers: []adk.ChatModelAgentMiddleware{
			&appendToolMiddleware{
				BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
				tool:                         userTool,
			},
		},
	})
	if err != nil {
		t.Fatalf("buildDeepCompatibleHandlers() error = %v", err)
	}

	runCtx := &adk.ChatModelAgentContext{}
	for _, handler := range handlers {
		var err error
		_, runCtx, err = handler.BeforeAgent(ctx, runCtx)
		if err != nil {
			t.Fatalf("BeforeAgent() error = %v", err)
		}
	}

	got := toolNames(t, ctx, runCtx.Tools)
	want := []string{"write_todos", "task", "user_tool"}
	if len(got) != len(want) {
		t.Fatalf("tool order length = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tool order = %v, want %v", got, want)
		}
	}
}

func toolNames(t *testing.T, ctx context.Context, tools []tool.BaseTool) []string {
	t.Helper()
	names := make([]string, 0, len(tools))
	for _, candidate := range tools {
		info, err := candidate.Info(ctx)
		if err != nil {
			t.Fatalf("tool Info() error = %v", err)
		}
		names = append(names, info.Name)
	}
	return names
}
