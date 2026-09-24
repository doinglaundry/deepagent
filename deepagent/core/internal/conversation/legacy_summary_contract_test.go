package conversation

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type legacyParitySummaryModel struct{}

func (legacyParitySummaryModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("goal and decision", nil), nil
}
func (legacyParitySummaryModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("unused")
}

func TestSummaryCompactionPreservesRecentToolExchange(t *testing.T) {
	strategy := &SummaryCompaction{Model: legacyParitySummaryModel{}, KeepRecent: 3, TokenLimit: 100}
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
	if result == nil || len(result.Rebuilt) != 5 || result.Rebuilt[2].ToolCalls[0].ID != "call" || result.Rebuilt[3].ToolCallID != "call" {
		t.Fatalf("compacted=%+v", result)
	}
	resumed, err := strategy.Resume(context.Background(), result.Compact, []*schema.Message{schema.UserMessage("later")})
	if err != nil || len(resumed.Rebuilt) != 6 {
		t.Fatalf("resumed=%+v err=%v", resumed, err)
	}
	if resumed.Rebuilt[2].ToolCalls[0].ID != "call" || resumed.Rebuilt[3].ToolCallID != "call" || resumed.Rebuilt[5].Content != "later" {
		t.Fatalf("resume lost retained exchange: %+v", resumed.Rebuilt)
	}
}
