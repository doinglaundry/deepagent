package graph

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestCollectorRepairsOnlyUnambiguousJSONAtStreamEnd(t *testing.T) {
	collector := NewToolCallCollector()
	collector.partialCalls["id_call"] = &schema.ToolCall{
		ID: "call", Function: schema.FunctionCall{Name: "read_file", Arguments: "```json\n{\"path\":\"a.go\",}\n```"},
	}
	calls := collector.GetRepairedToolCalls(context.Background())
	if len(calls) != 1 || calls[0].Function.Arguments != `{"path":"a.go"}` {
		t.Fatalf("repaired calls = %+v", calls)
	}

	collector.partialCalls["id_bad"] = &schema.ToolCall{
		ID: "bad", Function: schema.FunctionCall{Name: "read_file", Arguments: `{path:"a.go"}`},
	}
	for _, call := range collector.GetRepairedToolCalls(context.Background()) {
		if call.ID == "bad" {
			t.Fatal("ambiguous JSON was invented")
		}
	}
}
