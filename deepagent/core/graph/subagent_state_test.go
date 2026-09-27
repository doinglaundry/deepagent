package graph

import (
	"context"
	"eino-cli/deepagent/core/internal/conversation"

	"eino-cli/deepagent/core/tools"

	"github.com/cloudwego/eino/components/model"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"testing"
)

type childUsageModel struct {
	sequenceModel
	check func()
}

func (m *childUsageModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (m *childUsageModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if m.calls == 1 {
		m.check()
	}
	return m.sequenceModel.Stream(ctx, input, opts...)
}
func TestCheckpoint_ChildConversationRestoresProviderUsage(t *testing.T) {
	ctx := context.Background()
	counter := func(messages []*schema.Message) int { return len(messages) * 3 }
	initial := conversation.New("", nil, nil, counter)
	fresh := conversation.New("", nil, nil, counter)
	reply := schema.AssistantMessage("", []schema.ToolCall{{ID: "approval", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})
	reply.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 400, CompletionTokens: 10, TotalTokens: 410}}
	m := &childUsageModel{sequenceModel: sequenceModel{responses: [][]*schema.Message{{reply}, {schema.AssistantMessage("done", nil)}}}, check: func() {
		usage := fresh.ContextUsage()
		if usage.Source != conversation.ContextUsageSourceModelUsage || usage.LastModelTotal != 410 || usage.CurrentTotal != 413 || usage.EstimatedAfterLastModel != 3 {
			t.Errorf("lost provider baseline: %+v", usage)
		}
	}}
	cfg := Config{Depth: 1, RunID: "child-run", Model: m, Conversation: initial, CheckpointStore: &checkpointMemory{}, ToolDescriptors: []tools.ToolDescriptor{{Tool: &countingTool{}, RequiresApproval: true}}}
	a, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(ctx, []*schema.Message{schema.UserMessage("work")}, WithCheckpointID("checkpoint"))
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok {
		t.Fatal(err)
	}
	cfg.Conversation = fresh
	restored, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, err = restored.Run(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{Approved: true}}))
	if err != nil {
		t.Fatal(err)
	}
	if m.calls != 2 {
		t.Fatalf("model=%d", m.calls)
	}
}
