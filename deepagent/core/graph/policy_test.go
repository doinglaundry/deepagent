package graph

import (
	"context"
	"testing"

	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/schema"
)

func TestRun_LegacyPolicyGateDeniesWithoutExecution(t *testing.T) {
	tool := &countingTool{}
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}}}
	a, err := New(context.Background(), WithConfig(&Config{Model: m, ToolDescriptors: []tools.Descriptor{{Tool: tool, ReturnDirect: true}}, HITLConfig: &HITLConfig{NeedFollowUpTool: true, ToolPolicyGates: map[string]tools.ToolPolicyGate{
		"counter": {Policy: func(_ context.Context, info *tools.ApprovalInfo) (tools.ToolCallDecision, error) {
			if info.ToolName != "counter" || info.ArgumentsInJSON != "{}" {
				t.Fatalf("policy input=%+v", info)
			}
			return tools.ToolCallDecision{Action: tools.ToolCallDeny, Reason: "blocked"}, nil
		}},
	}}}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.Run(context.Background(), []*schema.Message{schema.UserMessage("go")})
	if err != nil {
		t.Fatal(err)
	}
	if tool.count.Load() != 0 || result.Content != "blocked" {
		t.Fatalf("executed=%d result=%v", tool.count.Load(), result)
	}
	if _, ok := a.registry.Lookup("ask_user"); !ok {
		t.Fatal("follow-up tool missing")
	}
}

func TestPolicy_ApprovalCannotOverrideGlobalDeny(t *testing.T) {
	p := policyWithGates(tools.PolicyFunc(func(context.Context, types.ToolCall, tools.Descriptor) (tools.Decision, error) {
		return tools.Decision{Action: tools.Deny, Reason: "global deny"}, nil
	}), map[string]tools.ToolPolicyGate{"counter": {Policy: func(context.Context, *tools.ApprovalInfo) (tools.ToolCallDecision, error) {
		return tools.ToolCallDecision{Action: string(tools.AskApproval)}, nil
	}}})
	decision, err := p.Decide(context.Background(), types.ToolCall{Name: "counter"}, tools.Descriptor{Tool: &countingTool{}})
	if err != nil || decision.Action != tools.Deny {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
}
