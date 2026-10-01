package deepagents

import (
	"context"
	"eino-cli/deepagent/core/types"
	"strings"
	"testing"

	"eino-cli/deepagent/core/tools"

	"github.com/cloudwego/eino/schema"
)

func TestRun_ReadOnlyToolSetCannotExecuteUnclassifiedTool(t *testing.T) {
	ctx := context.Background()
	tool := &countingTool{}
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}, {schema.AssistantMessage("unavailable", nil)}}}
	a, err := NewRun(ctx, WithConfig(&Config{Model: m, ReadOnlyToolsOnly: true, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool}}}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	_, err = a.Execute(ctx, []*schema.Message{schema.UserMessage("go")})
	if err != nil {
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

func TestPolicy_DenyPreventsExecutionAndApproval(t *testing.T) {
	for _, requiresApproval := range []bool{false, true} {
		counter := &countingTool{}
		m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}}}
		a, err := NewRun(context.Background(), WithConfig(&Config{
			Model:           m,
			ToolDescriptors: []tools.ToolDescriptor{{Tool: counter, RequiresApproval: requiresApproval, ReturnDirect: true}},
			Policy: tools.PolicyFunc(func(context.Context, types.ToolCall, tools.ToolDescriptor) (tools.Decision, error) {
				return tools.Decision{Action: tools.Deny, Reason: "blocked"}, nil
			}),
		}))
		if err != nil {
			t.Fatal(err)
		}
		result, err := a.Execute(context.Background(), []*schema.Message{schema.UserMessage("go")})
		if err != nil {
			t.Fatal(err)
		}
		if counter.count.Load() != 0 || result.Content != "blocked" {
			t.Fatalf("executed=%d result=%v", counter.count.Load(), result)
		}
		err = a.Close(context.Background())
		if err != nil {
			t.Fatal(err)
		}
	}
}
