package graph

import (
	"context"
	"testing"

	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/schema"
)

func TestRun_PolicyAndExecutionReceiveModelArguments(t *testing.T) {
	ctx := context.Background()
	tool := &countingTool{}
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{
		{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: `{"value":"hello"}`}},
	})}}}
	checked := 0
	a, err := New(ctx, WithConfig(&Config{
		Model: m,
		ToolDescriptors: []tools.Descriptor{{Tool: tool, ReturnDirect: true}},
		Policy: tools.PolicyFunc(func(_ context.Context, call types.ToolCall, _ tools.Descriptor) (tools.Decision, error) {
			checked++
			if call.Arguments != `{"value":"hello"}` {
				t.Fatalf("policy saw unexpected arguments: %q", call.Arguments)
			}
			return tools.Decision{Action: tools.Allow}, nil
		}),
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	answer, err := a.Run(ctx, []*schema.Message{schema.UserMessage("run")})
	if err != nil {
		t.Fatal(err)
	}
	if answer.Content != `{"value":"hello"}` || tool.count.Load() != 1 || m.calls != 1 || checked != 1 {
		t.Fatalf("answer=%+v executions=%d models=%d checked=%d", answer, tool.count.Load(), m.calls, checked)
	}
}
