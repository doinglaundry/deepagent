package deepagents

import (
	"context"
	"errors"
	"strings"

	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"

	"github.com/cloudwego/eino/schema"
	"reflect"
	"testing"
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

func TestMiddleware_OrderAndAfterRunOnce(t *testing.T) {
	ctx := context.Background()
	var order []string
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Type: "function", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}}}
	a, err := NewRun(ctx, WithConfig(&Config{Model: m, ToolDescriptors: []tools.ToolDescriptor{{Tool: &countingTool{}, ReturnDirect: true}}, Middlewares: []middleware.Middleware{&orderedMiddleware{name: "outer", order: &order}, &orderedMiddleware{name: "inner", order: &order}}}))
	if err != nil {
		t.Fatal(err)
	}
	{
		_, err := a.Execute(ctx, []*schema.Message{schema.UserMessage("go")})
		if err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"before:outer", "before:inner", "model:outer", "model:inner", "after:inner", "after:outer"}
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
	a, err := NewRun(ctx, WithConfig(&Config{Model: &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("done", nil)}}}, Middlewares: []middleware.Middleware{mw}, Emit: func(_ context.Context, e types.RuntimeEvent) error {
		if e.Kind == "turn_end" && !mw.after {
			t.Error("final event preceded AfterRun")
		}
		return nil
	}}))
	if err != nil {
		t.Fatal(err)
	}
	{
		_, err := a.Execute(ctx, []*schema.Message{schema.UserMessage("input")})
		if err != nil {
			t.Fatal(err)
		}
	}
}

type modelTransformMiddleware struct {
	middleware.BaseMiddleware
	before, after int
	failure       error
}

func (m *modelTransformMiddleware) ModifyModelRequest(_ context.Context, _ []*schema.Message, messages []*schema.Message, _ *types.GraphState) ([]*schema.Message, error) {
	m.before++
	if m.failure != nil {
		return nil, m.failure
	}
	return append([]*schema.Message{schema.SystemMessage("middleware prompt")}, messages...), nil
}

func (m *modelTransformMiddleware) ModifyModelStreamResponse(_ context.Context, stream *schema.StreamReader[*schema.Message], _ *types.GraphState) (*schema.StreamReader[*schema.Message], error) {
	m.after++
	return schema.StreamReaderWithConvert(stream, func(message *schema.Message) (*schema.Message, error) {
		copy := *message
		copy.Content = "rewritten"
		return &copy, nil
	}), nil
}

func TestRun_ModelMiddlewareModifiesRequestAndStream(t *testing.T) {
	ctx := context.Background()
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("original", nil)}}}
	mw := &modelTransformMiddleware{}
	a, err := NewRun(ctx, WithModel(m), WithMiddleware(mw))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	out, err := a.Execute(ctx, []*schema.Message{schema.UserMessage("input")})
	if err != nil {
		t.Fatal(err)
	}
	if mw.before != 1 || mw.after != 1 || out.Content != "rewritten" || m.inputs[0][0].Content != "middleware prompt" {
		t.Fatalf("middleware not applied: before=%d after=%d output=%v inputs=%v", mw.before, mw.after, out, m.inputs)
	}
	history := a.conversation.History(ctx)
	if history[len(history)-1].Content != "rewritten" {
		t.Fatal("history bypassed middleware")
	}
}

func TestRun_ModelMiddlewareErrorStopsModel(t *testing.T) {
	want := errors.New("before model failed")
	m := &sequenceModel{}
	a, err := NewRun(context.Background(), WithModel(m), WithMiddleware(&modelTransformMiddleware{failure: want}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(context.Background())
	_, err = a.Execute(context.Background(), []*schema.Message{schema.UserMessage("input")})
	if !errors.Is(err, want) || m.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, m.calls)
	}
}

func TestDuplicateStatefulMiddlewareNamesRejectedBeforeModelCall(t *testing.T) {
	tests := []struct {
		name        string
		middlewares []middleware.Middleware
	}{
		{name: "circuit breaker", middlewares: []middleware.Middleware{&middleware.CircuitBreaker{}, &middleware.CircuitBreaker{}}},
		{name: "loop guard", middlewares: []middleware.Middleware{middleware.NewLoopGuard(), middleware.NewLoopGuard()}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("unexpected", nil)}}}
			_, err := NewRun(context.Background(), WithConfig(&Config{Model: m, Middlewares: tc.middlewares}))
			if err == nil {
				t.Fatal("duplicate stateful middleware name was accepted")
			}
			if !strings.Contains(err.Error(), "duplicate stateful middleware name") {
				t.Fatalf("unexpected error: %v", err)
			}
			if m.calls != 0 {
				t.Fatalf("model calls=%d", m.calls)
			}
		})
	}
}

func TestDuplicateStatelessMiddlewareNamesRemainSupported(t *testing.T) {
	ctx := context.Background()
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("done", nil)}}}
	middlewares := []middleware.Middleware{
		&orderedMiddleware{name: "shared", order: new([]string)},
		&orderedMiddleware{name: "shared", order: new([]string)},
	}
	a, err := NewRun(ctx, WithConfig(&Config{Model: m, Middlewares: middlewares}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	result, err := a.Execute(ctx, []*schema.Message{schema.UserMessage("input")})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Content != "done" || m.calls != 1 {
		t.Fatalf("result=%v model calls=%d", result, m.calls)
	}
}
