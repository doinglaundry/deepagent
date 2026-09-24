package graph

import (
	"context"
	"reflect"
	"testing"

	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/schema"
)

type orderedMiddleware struct {
	middleware.BaseMiddleware
	name  string
	order *[]string
}

func (m *orderedMiddleware) Name() string { return m.name }
func (m *orderedMiddleware) BeforeRun(context.Context, *types.RunState) error {
	*m.order = append(*m.order, "before:"+m.name)
	return nil
}
func (m *orderedMiddleware) AfterRun(context.Context, *types.RunState, error) error {
	*m.order = append(*m.order, "after:"+m.name)
	return nil
}
func (m *orderedMiddleware) WrapModel(next middleware.ModelHandler) middleware.ModelHandler {
	return func(ctx context.Context, input []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		*m.order = append(*m.order, "model:"+m.name)
		return next(ctx, input)
	}
}
func (m *orderedMiddleware) WrapTool(next middleware.ToolHandler) middleware.ToolHandler {
	return func(ctx context.Context, call types.ToolCall) (*types.ToolResult, error) {
		*m.order = append(*m.order, "tool:"+m.name)
		return next(ctx, call)
	}
}
func TestMiddleware_OrderAndAfterRunOnce(t *testing.T) {
	ctx := context.Background()
	var order []string
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Type: "function", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}}}
	a, err := New(ctx, WithConfig(&Config{Model: m, ToolDescriptors: []tools.Descriptor{{Tool: &countingTool{}, ReturnDirect: true}}, Middlewares: []middleware.Middleware{&orderedMiddleware{name: "outer", order: &order}, &orderedMiddleware{name: "inner", order: &order}}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(ctx, []*schema.Message{schema.UserMessage("go")}); err != nil {
		t.Fatal(err)
	}
	want := []string{"before:outer", "before:inner", "model:outer", "model:inner", "tool:outer", "tool:inner", "after:inner", "after:outer"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("middleware order=%v want=%v", order, want)
	}
}

type endOrderMiddleware struct {
	middleware.BaseMiddleware
	after bool
}

func (*endOrderMiddleware) BeforeRun(context.Context, *types.RunState) error { return nil }
func (m *endOrderMiddleware) AfterRun(context.Context, *types.RunState, error) error {
	m.after = true
	return nil
}
func TestRun_FinalEventFollowsAfterRun(t *testing.T) {
	ctx := context.Background()
	mw := &endOrderMiddleware{}
	a, err := New(ctx, WithConfig(&Config{Model: &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("done", nil)}}}, Middlewares: []middleware.Middleware{mw}, Emit: func(_ context.Context, e types.RuntimeEvent) error {
		if e.Kind == "turn_end" && !mw.after {
			t.Error("final event preceded AfterRun")
		}
		return nil
	}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(ctx, []*schema.Message{schema.UserMessage("input")}); err != nil {
		t.Fatal(err)
	}
}
