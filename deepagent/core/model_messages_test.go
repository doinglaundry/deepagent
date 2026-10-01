package deepagents

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/cloudwego/eino/schema"
	"testing"
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

func TestRepairToolArgumentsOnlyUnambiguousSyntax(t *testing.T) {
	for _, s := range []string{"```json\n{\"path\":\"x\",}\n```", `{"a":[1,2,],"literal":",}"}`} {
		out, e := repairToolArguments(s)
		if e != nil || !json.Valid([]byte(out)) {
			t.Fatal(out, e)
		}
	}
	for _, s := range []string{`{path:"x"}`, `{"path":`, "text {\"a\":1}"} {
		{
			_, e := repairToolArguments(s)
			if e == nil {
				t.Fatal("invented missing JSON", s)
			}
		}
	}
}

func TestCollectorRepairsOnlyUnambiguousJSONAtStreamEnd(t *testing.T) {
	collector := &toolCallBuffer{}
	_, err := collector.add([]schema.ToolCall{{
		ID: "call", Function: schema.FunctionCall{Name: "read_file", Arguments: "```json\n{\"path\":\"a.go\",}\n```"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	calls, err := collector.finish()
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Function.Arguments != `{"path":"a.go"}` {
		t.Fatalf("repaired calls = %+v", calls)
	}

	_, err = collector.add([]schema.ToolCall{{
		ID: "bad", Function: schema.FunctionCall{Name: "read_file", Arguments: `{path:"a.go"}`},
	}})
	if err != nil {
		t.Fatal(err)
	}
	calls, err = collector.finish()
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[1].Function.Arguments != `{path:"a.go"}` {
		t.Fatalf("ambiguous JSON was invented or silently dropped: %+v", calls)
	}
}
