package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"eino-cli/deepagent/core/graph"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

type promptService struct {
	summary string
	reads   int
	err     error
}

func (s *promptService) Read(_ context.Context, scope string) (*Snapshot, error) {
	s.reads++
	return &Snapshot{Scope: scope, Summary: s.summary}, s.err
}
func (*promptService) Observe(context.Context, string, string, []*schema.Message) error { return nil }
func (*promptService) Consolidate(context.Context, string) error                        { return nil }

type promptModel struct{ inputs [][]*schema.Message }

func (m *promptModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (*promptModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("bypassed Graph")
}
func (m *promptModel) Stream(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.inputs = append(m.inputs, messages)
	if len(m.inputs) == 1 {
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
	m := &promptModel{}
	agent, err := graph.New(ctx, graph.WithConfig(&graph.Config{Model: m, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool}}, Middlewares: []middleware.Middleware{NewPrompt(service, "user/one")}}))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(ctx)
	if _, err := agent.Run(ctx, []*schema.Message{schema.UserMessage("go")}); err != nil {
		t.Fatal(err)
	}
	if service.reads != 2 || len(m.inputs) != 2 {
		t.Fatalf("reads=%d model=%d", service.reads, len(m.inputs))
	}
	for i, want := range []string{"first snapshot", "second snapshot"} {
		count := 0
		for _, message := range m.inputs[i] {
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
	m := &promptModel{}
	a, err := graph.New(context.Background(), graph.WithConfig(&graph.Config{Model: m, Middlewares: []middleware.Middleware{NewPrompt(service, "user/one")}}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(context.Background())
	_, err = a.Run(context.Background(), []*schema.Message{schema.UserMessage("go")})
	if !errors.Is(err, want) || len(m.inputs) != 0 {
		t.Fatalf("err=%v model=%d", err, len(m.inputs))
	}
}
