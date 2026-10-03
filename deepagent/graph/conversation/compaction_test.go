package conversation

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestBootstrapReplacementPreservesSummaryAndHistoricalSource(t *testing.T) {
	for _, tc := range []struct {
		name, first         string
		enabled, withPrompt bool
		wantCount           int
	}{
		{"replace stale bootstrap", "old prompt", true, true, 2},
		{"preserve leading summary", "Earlier conversation summary: important", true, true, 3},
		{"default keeps system history", "historical instruction", false, true, 3},
		{"no replacement prompt keeps history", "old prompt", true, false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			c := New("thread", nil, nil, nil, WithBootstrapPromptReplacement(tc.enabled))
			original := schema.SystemMessage(tc.first)
			addHistoryErr := c.AddHistory(ctx, "old", original, schema.UserMessage("prior"))
			if addHistoryErr != nil {
				t.Fatal(addHistoryErr)
			}
			var prompts []*schema.Message
			if tc.withPrompt {
				prompts = []*schema.Message{schema.SystemMessage("fresh prompt")}
			}
			request, err := c.BuildRequest(ctx, prompts)
			if err != nil {
				t.Fatal(err)
			}
			if len(request) != tc.wantCount {
				t.Fatalf("request=%v", request)
			}
			if tc.withPrompt && request[0].Content != "fresh prompt" {
				t.Fatal("fresh prompt lost")
			}
			if tc.name == "preserve leading summary" && request[1].Content != tc.first {
				t.Fatal("summary discarded")
			}
			history := c.History(ctx)
			if len(history) != 2 || history[0] != original || original.Content != tc.first {
				t.Fatal("request projection mutated source history")
			}
		})
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
	if result == nil || len(result.Rebuilt) != 5 || result.Rebuilt[2].ToolCalls[0].ID != "call" || result.Rebuilt[3].ToolCallID != "call" {
		t.Fatalf("compacted=%+v", result)
	}
	store := &testStore{}
	live := New("thread", store, strategy, nil)
	err = live.AddHistory(context.Background(), "run", current...)
	if err != nil {
		t.Fatal(err)
	}
	_, err = live.Compact(context.Background(), "run")
	if err != nil {
		t.Fatal(err)
	}
	err = live.AddHistory(context.Background(), "run", schema.UserMessage("later"))
	if err != nil {
		t.Fatal(err)
	}
	restored := New("thread", store, nil, nil)
	err = restored.ReloadHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	resumed := restored.History(context.Background())
	if len(resumed) != 6 || resumed[2].ToolCalls[0].ID != "call" || resumed[3].ToolCallID != "call" || resumed[5].Content != "later" {
		t.Fatalf("durable reload lost retained exchange: %+v", resumed)
	}
}
