package graph

import (
	"context"
	"errors"
	"testing"

	hook "eino-cli/deepagent/core/hooks"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/schema"
)

func TestRun_ModelHooksModifyRequestAndStream(t *testing.T) {
	ctx := context.Background()
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("original", nil)}}}
	before, after := 0, 0
	a, err := New(ctx, WithModel(m), WithHooks(hook.Hooks{
		BeforeModel: func(_ context.Context, initial, messages []*schema.Message, state *types.GraphState) ([]*schema.Message, error) {
			before++
			if len(initial) != 1 || initial[0].Content != "input" || state == nil {
				t.Fatal("missing initial input or graph state")
			}
			return append([]*schema.Message{schema.SystemMessage("hook prompt")}, messages...), nil
		},
		AfterModel: func(_ context.Context, output hook.ModelOutput, _ *types.GraphState) (hook.ModelOutput, error) {
			after++
			if !output.IsStream || output.Stream == nil {
				t.Fatal("hook did not receive model stream")
			}
			output.Stream = schema.StreamReaderWithConvert(output.Stream, func(m *schema.Message) (*schema.Message, error) {
				copy := *m
				copy.Content = "rewritten"
				return &copy, nil
			})
			return output, nil
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.Run(ctx, []*schema.Message{schema.UserMessage("input")})
	if err != nil {
		t.Fatal(err)
	}
	if before != 1 || after != 1 || out.Content != "rewritten" || m.inputs[0][0].Content != "hook prompt" {
		t.Fatalf("hooks not applied: before=%d after=%d output=%v inputs=%v", before, after, out, m.inputs)
	}
	if history := a.conversation.History(ctx); history[len(history)-1].Content != "rewritten" {
		t.Fatal("history bypassed model hook")
	}
}

func TestRun_BeforeModelErrorStopsModel(t *testing.T) {
	want := errors.New("before model failed")
	m := &sequenceModel{}
	a, err := New(context.Background(), WithModel(m), WithHooks(hook.Hooks{BeforeModel: func(context.Context, []*schema.Message, []*schema.Message, *types.GraphState) ([]*schema.Message, error) {
		return nil, want
	}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(context.Background(), []*schema.Message{schema.UserMessage("input")})
	if !errors.Is(err, want) || m.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, m.calls)
	}
}
