package graph

import (
	"context"
	"testing"

	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/schema"
)

func TestRun_ArgumentNormalizationPrecedesPolicyAndExecution(t *testing.T) {
	ctx := context.Background()
	tool := &countingTool{}
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{
		{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: `{"value":"hello"}`}},
	})}}}
	normalized, checked := 0, 0
	a, err := New(ctx, WithConfig(&Config{
		Model: m,
		ToolDescriptors: []tools.Descriptor{{Tool: tool, ReturnDirect: true, NormalizeArgs: func(raw string) (string, error) {
			normalized++
			if raw != `{"value":"hello"}` {
				t.Fatalf("raw arguments=%q", raw)
			}
			return `{"value":"rewritten"}`, nil
		}}},
		Policy: tools.PolicyFunc(func(_ context.Context, call types.ToolCall, _ tools.Descriptor) (tools.Decision, error) {
			checked++
			if call.Arguments != `{"value":"rewritten"}` {
				t.Fatalf("policy saw unnormalized arguments: %q", call.Arguments)
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
	if answer.Content != `{"value":"rewritten"}` || tool.count.Load() != 1 || m.calls != 1 || normalized != 1 || checked != 1 {
		t.Fatalf("answer=%+v executions=%d models=%d normalized=%d checked=%d", answer, tool.count.Load(), m.calls, normalized, checked)
	}
}
