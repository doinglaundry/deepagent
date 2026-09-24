package graph

import (
	"context"
	"strings"
	"testing"

	"eino-cli/deepagent/core/internal/conversation"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type paritySummaryModel struct{ sequenceModel }

func (*paritySummaryModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("retained summary", nil), nil
}

func TestRun_AutomaticThresholdCompactionRetainsRecentInput(t *testing.T) {
	ctx := context.Background()
	history := conversation.New("thread", nil, &conversation.SummaryCompaction{Model: &paritySummaryModel{}, TokenLimit: 1, KeepRecent: 4}, nil)
	for range 8 {
		if err := history.AddHistory(ctx, "old", schema.UserMessage("old user"), schema.AssistantMessage("old answer", nil)); err != nil {
			t.Fatal(err)
		}
	}
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("done", nil)}}}
	compacted := false
	a, err := New(ctx, WithConfig(&Config{Model: m, Conversation: history, Emit: func(_ context.Context, e types.RuntimeEvent) error {
		if e.Kind == "context_compacted" {
			compacted = true
		}
		return nil
	}}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	if _, err = a.Run(ctx, []*schema.Message{schema.UserMessage("newest")}); err != nil {
		t.Fatal(err)
	}
	input := m.inputs[0]
	if !compacted || len(input) > 7 || input[0].Content != "Earlier conversation summary:\nretained summary" || input[len(input)-1].Content != "newest" {
		t.Fatalf("compaction lost recent input: %v", input)
	}
}

func TestRun_ReadOnlyRegistryCannotExecuteUnclassifiedTool(t *testing.T) {
	ctx := context.Background()
	tool := &countingTool{}
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}, {schema.AssistantMessage("unavailable", nil)}}}
	a, err := New(ctx, WithConfig(&Config{Model: m, ReadOnlyToolsOnly: true, ToolDescriptors: []tools.Descriptor{{Tool: tool}}}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	if _, err = a.Run(ctx, []*schema.Message{schema.UserMessage("go")}); err != nil {
		t.Fatal(err)
	}
	if tool.count.Load() != 0 {
		t.Fatal("readonly run executed unclassified tool")
	}
	last := m.inputs[1][len(m.inputs[1])-1]
	if last.Role != schema.Tool || !strings.Contains(last.Content, "unknown tool") {
		t.Fatalf("model did not see unavailable tool: %+v", last)
	}
}
