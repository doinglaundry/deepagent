package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"eino-cli/deepagent/graph/execution"
	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

type promptService struct {
	summary string
	reads   int
	err     error
}

func (promptService *promptService) Read(_ context.Context, scope string) (*agentmodel.MemorySnapshot, error) {
	promptService.reads++
	return &agentmodel.MemorySnapshot{Scope: scope, Summary: promptService.summary}, promptService.err
}

func (*promptService) Observe(context.Context, string, string, []*agentmodel.Message) error {
	return nil
}

func (*promptService) Consolidate(context.Context, string) error { return nil }

type promptModel struct{ inputs [][]*schema.Message }

func (chatModel *promptModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return chatModel, nil
}

func (*promptModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("bypassed Graph")
}

func (chatModel *promptModel) Stream(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	chatModel.inputs = append(chatModel.inputs, messages)
	if len(chatModel.inputs) == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("", []schema.ToolCall{{ID: "change", Function: schema.FunctionCall{Name: "change_memory", Arguments: "{}"}}})}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("done", nil)}), nil
}

func TestMemoryPrompt_ReadsCurrentScopeBeforeEachModel(t *testing.T) {
	ctx := context.Background()
	service := &promptService{summary: "first snapshot"}
	tool, err := utils.InferTool("change_memory", "Update memory", func(context.Context, struct{}) (string, error) {
		service.summary = "second snapshot"
		return "updated", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	chatModel := &promptModel{}
	graph, err := execution.New(ctx, execution.WithConfig(&execution.Config{Model: chatModel, ToolDescriptors: []agentmodel.ToolDescriptor{{Tool: tool}}, Middlewares: []agentmodel.Middleware{NewPrompt(service, "user/one")}}))
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close(ctx)
	_, executeErr := graph.Invoke(ctx, []*agentmodel.Message{agentmodel.NewUserMessage("go")})
	if executeErr != nil {
		t.Fatal(executeErr)
	}
	if service.reads != 2 || len(chatModel.inputs) != 2 {
		t.Fatalf("reads=%d model=%d", service.reads, len(chatModel.inputs))
	}
	for i, want := range []string{"first snapshot", "second snapshot"} {
		count := 0
		for _, message := range chatModel.inputs[i] {
			if strings.Contains(message.Content, "Prior memory") {
				count++
				if !strings.Contains(message.Content, want) {
					t.Fatalf("stale snapshot: %s", message.Content)
				}
			}
		}
		if count != 1 {
			t.Fatalf("memory duplicated in history: request=%d count=%d", i, count)
		}
	}
}

func TestMemoryPrompt_ReadFailurePreventsModel(t *testing.T) {
	want := errors.New("memory store unavailable")
	service := &promptService{err: want}
	chatModel := &promptModel{}
	graph, err := execution.New(context.Background(), execution.WithConfig(&execution.Config{Model: chatModel, Middlewares: []agentmodel.Middleware{NewPrompt(service, "user/one")}}))
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close(context.Background())
	_, err = graph.Invoke(context.Background(), []*agentmodel.Message{agentmodel.NewUserMessage("go")})
	if !errors.Is(err, want) || len(chatModel.inputs) != 0 {
		t.Fatalf("err=%v model=%d", err, len(chatModel.inputs))
	}
}
