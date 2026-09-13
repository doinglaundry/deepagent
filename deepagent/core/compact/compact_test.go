package compact

import (
	"context"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"testing"
)

type summarizer struct{}

func (summarizer) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("summary", nil), nil
}
func (summarizer) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("unused")
}
func TestCompactionPreservesToolPairs(t *testing.T) {
	ms := []*schema.Message{schema.SystemMessage("system"), schema.UserMessage("old"), schema.AssistantMessage("old result", nil), schema.UserMessage("new"), {Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "id"}}}, {Role: schema.Tool, ToolCallID: "id"}, schema.AssistantMessage("new result", nil)}
	out, rec, e := Summarize(context.Background(), summarizer{}, ms, 2)
	if e != nil {
		t.Fatal(e)
	}
	if rec.Removed != 3 {
		t.Fatal(rec)
	}
	if len(out) != 6 || out[3].ToolCalls[0].ID != "id" || out[4].ToolCallID != "id" {
		t.Fatalf("%+v", out)
	}
}

func TestRepeatedCompactionMergesExistingSummary(t *testing.T) {
	ms := []*schema.Message{schema.SystemMessage("bootstrap"), schema.SystemMessage("Earlier conversation summary:\nolder"), schema.UserMessage("old"), schema.AssistantMessage("old reply", nil), schema.UserMessage("new"), schema.AssistantMessage("new reply", nil)}
	ms[2].Extra = map[string]any{"deepagent_input_id": "input-old"}
	out, _, e := Summarize(context.Background(), summarizer{}, ms, 2)
	if e != nil {
		t.Fatal(e)
	}
	if len(out) != 4 {
		t.Fatal("accumulated prior summary", len(out))
	}
	if out[1].Extra == nil {
		t.Fatal("lost input dedup metadata")
	}
}
