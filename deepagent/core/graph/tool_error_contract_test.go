package graph

import (
	"context"
	"errors"
	"strings"
	"testing"

	"eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type failingContractTool struct {
	countingTool
	failure error
}

func (t *failingContractTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "", t.failure
}

func TestRun_ToolErrorVisibleButCancellationStopsGraph(t *testing.T) {
	for _, failure := range []error{errors.New("ordinary tool failure"), context.Canceled, context.DeadlineExceeded} {
		t.Run(failure.Error(), func(t *testing.T) {
			m := &sequenceModel{responses: [][]*schema.Message{
				{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})},
				{schema.AssistantMessage("handled", nil)},
			}}
			a, err := New(context.Background(), WithConfig(&Config{Model: m, ToolDescriptors: []tools.Descriptor{{Tool: &failingContractTool{failure: failure}}}}))
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close(context.Background())
			out, err := a.Run(context.Background(), []*schema.Message{schema.UserMessage("go")})
			if errors.Is(failure, context.Canceled) || errors.Is(failure, context.DeadlineExceeded) {
				if !errors.Is(err, failure) || m.calls != 1 {
					t.Fatalf("cancellation swallowed: err=%v calls=%d", err, m.calls)
				}
				return
			}
			if err != nil || out == nil || out.Content != "handled" || m.calls != 2 {
				t.Fatalf("ordinary error aborted Graph: out=%v err=%v calls=%d", out, err, m.calls)
			}
			found := false
			for _, message := range m.inputs[1] {
				if message.Role == schema.Tool && message.ToolCallID == "call" && strings.Contains(message.Content, failure.Error()) {
					found = true
				}
			}
			if !found {
				t.Fatal("tool error not visible in next model request")
			}
		})
	}
}
