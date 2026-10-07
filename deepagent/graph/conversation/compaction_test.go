package conversation

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestBuildRequestPreservesHistoricalMessages(t *testing.T) {
	ctx := context.Background()
	conversation := New("thread", nil, nil, nil)
	original := schema.SystemMessage("historical instruction")
	err := conversation.AddHistory(ctx, "old", original, schema.UserMessage("prior"))
	if err != nil {
		t.Fatal(err)
	}
	request, err := conversation.BuildRequest(ctx, []*schema.Message{schema.SystemMessage("fresh prompt")})
	if err != nil {
		t.Fatal(err)
	}
	if len(request) != 3 || request[0].Content != "fresh prompt" || request[1] != original {
		t.Fatalf("request lost prompt or history: %v", request)
	}
	request[1] = schema.SystemMessage("replacement")
	history := conversation.GetHistory(ctx)
	if len(history) != 2 || history[0] != original || original.Content != "historical instruction" {
		t.Fatal("request projection mutated source history")
	}
}

type summaryModel struct{}

func (summaryModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("goal and decision", nil), nil
}
func (summaryModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("unused")
}

func TestSummaryCompactionPreservesRecentToolExchange(t *testing.T) {
	strategy := &SummaryCompaction{Model: summaryModel{}, KeepRecent: 3, TokenLimit: 100}
	current := []*schema.Message{
		schema.UserMessage("old"), schema.AssistantMessage("old answer", nil),
		schema.UserMessage("new"),
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "read"}}}},
		schema.ToolMessage("result", "call"), schema.AssistantMessage("done", nil),
	}
	result, err := strategy.Compact(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || len(result) != 5 || result[2].ToolCalls[0].ID != "call" || result[3].ToolCallID != "call" {
		t.Fatalf("compacted=%+v", result)
	}
	store := &testStore{}
	liveConversation := New("thread", store, strategy, nil)
	err = liveConversation.AddHistory(context.Background(), "run", current...)
	if err != nil {
		t.Fatal(err)
	}
	_, err = liveConversation.Compact(context.Background(), "run")
	if err != nil {
		t.Fatal(err)
	}
	err = liveConversation.AddHistory(context.Background(), "run", schema.UserMessage("later"))
	if err != nil {
		t.Fatal(err)
	}
	restoredConversation := New("thread", store, nil, nil)
	err = restoredConversation.ReloadHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	restoredHistory := restoredConversation.GetHistory(context.Background())
	if len(restoredHistory) != 6 || restoredHistory[2].ToolCalls[0].ID != "call" || restoredHistory[3].ToolCallID != "call" || restoredHistory[5].Content != "later" {
		t.Fatalf("durable reload lost retained exchange: %+v", restoredHistory)
	}
}
