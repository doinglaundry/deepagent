package graph

import (
	"context"
	"encoding/json"
	"testing"

	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type imageTool struct{ calls int }

func (*imageTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "image"}, nil
}
func (t *imageTool) InvokableRun(context.Context, *schema.ToolArgument, ...einotool.Option) (*schema.ToolResult, error) {
	t.calls++
	url := "https://example.test/image.png"
	return &schema.ToolResult{Parts: []schema.ToolOutputPart{
		{Type: schema.ToolPartTypeText, Text: "image description"},
		{Type: schema.ToolPartTypeImage, Image: &schema.ToolOutputImage{MessagePartCommon: schema.MessagePartCommon{URL: &url}}},
	}}, nil
}
func TestRun_EnhancedToolPreservesMultimodalHistoryAndState(t *testing.T) {
	tool := &imageTool{}
	m := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "image-call", Function: schema.FunctionCall{Name: "image", Arguments: "{}"}}})},
		{schema.AssistantMessage("done", nil)},
	}}
	var completed types.ToolCallState
	a, err := New(context.Background(), WithConfig(&Config{Model: m, ToolDescriptors: []tools.Descriptor{{Tool: tool}}, Emit: func(_ context.Context, event types.RuntimeEvent) error {
		if event.Kind == "tool_end" {
			completed = event.Data.(types.ToolCallState)
		}
		return nil
	}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Run(context.Background(), []*schema.Message{schema.UserMessage("show image")}); err != nil {
		t.Fatal(err)
	}
	if tool.calls != 1 || m.calls != 2 {
		t.Fatalf("tool=%d model=%d", tool.calls, m.calls)
	}
	var found *schema.Message
	for _, msg := range m.inputs[1] {
		if msg.ToolCallID == "image-call" {
			found = msg
		}
	}
	if found == nil || len(found.UserInputMultiContent) != 2 || found.UserInputMultiContent[1].Image == nil || *found.UserInputMultiContent[1].Image.URL != "https://example.test/image.png" {
		t.Fatalf("model lost image: %+v", found)
	}
	raw, err := json.Marshal(&types.RunState{Calls: []types.ToolCallState{completed}})
	if err != nil {
		t.Fatal(err)
	}
	var restored types.RunState
	if err = json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if len(restored.Calls) != 1 || restored.Calls[0].Result == nil || len(restored.Calls[0].Result.MultiContent) != 2 || *restored.Calls[0].Result.MultiContent[1].Image.URL != "https://example.test/image.png" {
		t.Fatalf("missing tool result: %s", raw)
	}
}

type enhancedMiddleware struct{ middleware.BaseMiddleware }

func (*enhancedMiddleware) Name() string { return "enhanced" }
func (*enhancedMiddleware) ToolCallMiddlewares() []compose.ToolMiddleware {
	return []compose.ToolMiddleware{{EnhancedInvokable: func(next compose.EnhancedInvokableToolEndpoint) compose.EnhancedInvokableToolEndpoint {
		return func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedInvokableToolOutput, error) {
			copy := *input
			copy.Arguments = `{"modified":true}`
			if _, err := next(ctx, &copy); err != nil {
				return nil, err
			}
			output, err := next(ctx, &copy)
			if err != nil {
				return nil, err
			}
			output.Result.Parts[0].Text = "wrapped:" + output.Result.Parts[0].Text
			return output, nil
		}
	}}}
}
func TestRun_EnhancedMiddlewareSharesPolicyAndExecutionLedger(t *testing.T) {
	for _, deny := range []bool{false, true} {
		tool := &imageTool{}
		m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "image-call", Function: schema.FunctionCall{Name: "image", Arguments: "{}"}}})}}}
		policies := 0
		a, err := New(context.Background(), WithConfig(&Config{Model: m, ToolDescriptors: []tools.Descriptor{{Tool: tool, ReturnDirect: true}}, Middlewares: []middleware.Middleware{&enhancedMiddleware{}}, Policy: tools.PolicyFunc(func(_ context.Context, call types.ToolCall, _ tools.Descriptor) (tools.Decision, error) {
			policies++
			if call.Arguments != `{"modified":true}` {
				t.Errorf("policy saw original arguments: %s", call.Arguments)
			}
			if deny {
				return tools.Decision{Action: tools.Deny, Reason: "denied"}, nil
			}
			return tools.Decision{Action: tools.Allow}, nil
		})}))
		if err != nil {
			t.Fatal(err)
		}
		message, err := a.Run(context.Background(), []*schema.Message{schema.UserMessage("show image")})
		if err != nil {
			t.Fatal(err)
		}
		want, calls, parts := "wrapped:image description", 1, 2
		if deny {
			want, calls, parts = "wrapped:denied", 0, 1
		}
		if tool.calls != calls || m.calls != 1 || policies != 1 || message.Content != want || len(message.UserInputMultiContent) != parts || message.UserInputMultiContent[0].Text != want {
			t.Fatalf("tool=%d model=%d policy=%d message=%+v", tool.calls, m.calls, policies, message)
		}
		history := a.conversation.History(context.Background())
		if history[len(history)-1].Role != schema.Tool {
			t.Fatal("ReturnDirect mutated tool history")
		}
	}
}
