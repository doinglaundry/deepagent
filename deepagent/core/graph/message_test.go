package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestStreamMergerPreservesInterleavedCallsAndFinalUsage(t *testing.T) {
	first, second := 90, 3
	chunks := []*schema.Message{
		nil,
		{Role: schema.Assistant, Content: "thinking", ToolCalls: []schema.ToolCall{
			{Index: &first, Function: schema.FunctionCall{Name: "read_file", Arguments: `{"path":`}},
			{Index: &second, ID: "second", Function: schema.FunctionCall{Name: "read_file", Arguments: `{"path":"b"}`}},
		}},
		{Content: " done", ReasoningContent: "reason", ToolCalls: []schema.ToolCall{
			{Index: &first, ID: "first", Function: schema.FunctionCall{Arguments: `"a"}`}},
			{Index: &second, Function: schema.FunctionCall{Arguments: " "}},
		}},
		{ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{TotalTokens: 12}}},
	}
	seen := 0
	merger := NewStreamMessageMerger(func(context.Context, *schema.Message) { seen++ })
	stream := schema.StreamReaderFromArray(chunks)
	defer stream.Close()
	message, err := merger.Merge(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	if seen != 3 || message.Content != "thinking done" || message.ReasoningContent != "reason" || message.ResponseMeta.Usage.TotalTokens != 12 {
		t.Fatalf("lost model output: seen=%d message=%+v", seen, message)
	}
	if len(message.ToolCalls) != 2 || message.ToolCalls[0].ID != "first" || message.ToolCalls[1].ID != "second" {
		t.Fatalf("tool identity or order lost: %+v", message.ToolCalls)
	}
	for i, call := range message.ToolCalls {
		if call.Index == nil || *call.Index != i {
			t.Fatalf("provider index leaked: %+v", call)
		}
	}
	if message.ToolCalls[0].Function.Arguments != `{"path":"a"}` {
		t.Fatalf("fragment lost: %+v", message.ToolCalls[0])
	}
	// Reusing a callback merger cannot retain calls from its previous stream.
	next := schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("new", nil)})
	defer next.Close()
	message, err = merger.Merge(context.Background(), next)
	if err != nil || message.Content != "new" || len(message.ToolCalls) != 0 {
		t.Fatalf("cross-stream state: message=%+v err=%v", message, err)
	}
}

func TestStreamMergerPropagatesStreamFailure(t *testing.T) {
	want := errors.New("provider stream failed")
	stream, writer := schema.Pipe[*schema.Message](1)
	writer.Send(nil, want)
	writer.Close()
	defer stream.Close()
	_, err := NewStreamMessageMerger(nil).Merge(context.Background(), stream)
	if !errors.Is(err, want) {
		t.Fatalf("stream error swallowed: %v", err)
	}
}
