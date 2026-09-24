package graph

import (
	"context"
	"errors"
	"testing"

	"eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestRun_ToolNodeHooksPreserveApprovalResumeAndReturnDirect(t *testing.T) {
	ctx := context.Background()
	tool := &countingTool{}
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "original"}}})}}}
	pre, post := 0, 0
	cfg := Config{Model: m, RunID: "run", CheckpointStore: &checkpointMemory{}, ToolDescriptors: []tools.Descriptor{{Tool: tool, ReturnDirect: true, RequiresApproval: true}},
		ToolNodePreHandler: func(_ context.Context, message *schema.Message) (*schema.Message, error) {
			pre++
			message.ToolCalls[0].Function.Arguments += ":pre"
			return message, nil
		},
		ToolNodePostHandler: func(_ context.Context, messages []*schema.Message) ([]*schema.Message, error) {
			post++
			messages[0].Content += ":post"
			return messages, nil
		},
	}
	a, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok {
		t.Fatal(err)
	}
	approval := info.InterruptContexts[0].Info.(*tools.ApprovalInfo)
	if approval.Arguments != "original:pre" || pre != 1 || post != 0 || tool.count.Load() != 0 {
		t.Fatalf("approval=%+v pre=%d post=%d tools=%d", approval, pre, post, tool.count.Load())
	}
	cfg.Conversation = a.conversation
	restored, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	out, err := restored.Run(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{Approved: true}}))
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "original:pre:post" || pre != 1 || post != 1 || tool.count.Load() != 1 || m.calls != 1 {
		t.Fatalf("out=%v pre=%d post=%d tool=%d model=%d", out, pre, post, tool.count.Load(), m.calls)
	}
	history := restored.conversation.History(ctx)
	if history[1].ToolCalls[0].Function.Arguments != "original" || history[2].Content != out.Content {
		t.Fatal("pre hook mutated original history or post result was not persisted")
	}
}

func TestRun_PreHookFailurePreventsEagerExecution(t *testing.T) {
	want := errors.New("pre failed")
	tool := &countingTool{}
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}}}
	a, err := New(context.Background(), WithConfig(&Config{Model: m, EnableStreamToolCall: true, ToolDescriptors: []tools.Descriptor{{Tool: tool, ParallelSafe: true}}, ToolNodePreHandler: func(context.Context, *schema.Message) (*schema.Message, error) { return nil, want }}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(context.Background(), []*schema.Message{schema.UserMessage("go")})
	if !errors.Is(err, want) || tool.count.Load() != 0 {
		t.Fatalf("err=%v executed=%d", err, tool.count.Load())
	}
}
