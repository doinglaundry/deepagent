package graph

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

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
