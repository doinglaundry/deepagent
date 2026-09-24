package deepagents

import (
	"context"
	"errors"
	"testing"

	"eino-cli/deepagent/core/graph"
	hook "eino-cli/deepagent/core/hooks"
	"eino-cli/deepagent/core/internal/conversation"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func buildCreateConfig(opts ...Option) *Config {
	cfg := &Config{}
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

type publicModel struct {
	infos  []*schema.ToolInfo
	inputs [][]*schema.Message
	call   bool
}

func (m *publicModel) WithTools(infos []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.infos = infos
	return m, nil
}
func (m *publicModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("unexpected Generate")
}
func (m *publicModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.inputs = append(m.inputs, input)
	message := schema.AssistantMessage("done", nil)
	if m.call && len(m.inputs) == 1 {
		message = schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: `{"delta":3}`}}})
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func TestPublicEntryUsesCanonicalAgentAndContext(t *testing.T) {
	ctx := context.Background()
	m := &publicModel{}
	var agent *DeepAgent
	seen := false
	hooks := hook.Hooks{BeforeModel: func(ctx context.Context, _, messages []*schema.Message, _ *types.GraphState) ([]*schema.Message, error) {
		seen = true
		if GetDeepAgent(ctx) != agent || GetWholeGraphState(ctx) != agent.GraphState() {
			t.Error("public context lookup lost canonical agent")
		}
		return messages, nil
	}}
	var err error
	agent, err = New(ctx, WithModel(m), WithHooks(hooks))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(ctx)
	var canonical *graph.DeepAgent = agent
	if canonical != agent {
		t.Fatal("extra agent wrapper")
	}
	if _, err = agent.Run(ctx, []*schema.Message{schema.UserMessage("go")}); err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Fatal("hook not called")
	}
}
func TestPublicResumeOptionsMergeWithoutMutatingCaller(t *testing.T) {
	data := map[string]any{"first": 1}
	var opts RunOptions
	WithResumeData(data)(&opts)
	WithResumeData(map[string]any{"second": 2})(&opts)
	if len(data) != 1 || len(opts.ResumeData) != 2 {
		t.Fatal("resume options lost prior answers or mutated caller")
	}
}

type promptContractMiddleware struct {
	middleware.BaseMiddleware
	seen bool
}

func (m *promptContractMiddleware) Name() string { return "prompt_contract" }
func (m *promptContractMiddleware) BuildPrompt(context.Context) ([]*schema.Message, error) {
	return []*schema.Message{schema.SystemMessage("instructions")}, nil
}
func (m *promptContractMiddleware) ModifyModelRequest(_ context.Context, initial, messages []*schema.Message, _ *types.GraphState) ([]*schema.Message, error) {
	if len(initial) != 1 || initial[0].Role != schema.System {
		return nil, errors.New("initialContext no longer contains middleware prompts")
	}
	m.seen = true
	return messages, nil
}
func TestPublicConversationDoesNotDuplicateHistory(t *testing.T) {
	ctx := context.Background()
	m := &publicModel{call: true}
	mw := &promptContractMiddleware{}
	history := conversation.New("", nil, nil, nil)
	a, err := New(ctx, WithConfig(&Config{Model: m, Conversation: history}), WithMiddleware(mw), WithTools(&fakeToolCounter{}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	if _, err = a.Run(ctx, []*schema.Message{schema.UserMessage("go")}); err != nil {
		t.Fatal(err)
	}
	if len(history.History(ctx)) != 4 {
		t.Fatalf("conversation history = %v", history.History(ctx))
	}
	if !mw.seen || len(m.inputs) != 2 || len(m.inputs[0]) != 2 || len(m.inputs[1]) != 4 {
		t.Fatalf("history duplicated: %v", m.inputs)
	}
	if m.inputs[1][0].Role != schema.System || m.inputs[1][1].Content != "go" || m.inputs[1][2].Role != schema.Assistant || m.inputs[1][3].Role != schema.Tool {
		t.Fatal("history order changed")
	}
}
