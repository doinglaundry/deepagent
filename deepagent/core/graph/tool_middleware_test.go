package graph

import (
	"context"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"strings"
	"testing"
)

type nativeToolMiddleware struct {
	middleware.BaseMiddleware
	name   string
	repeat bool
}

type nativeStreamTool struct {
	calls     int
	arguments string
}

func (*nativeStreamTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "stream_native"}, nil
}
func (t *nativeStreamTool) StreamableRun(_ context.Context, args string, _ ...einotool.Option) (*schema.StreamReader[string], error) {
	t.calls++
	t.arguments = args
	return schema.StreamReaderFromArray([]string{"one", "two"}), nil
}

type nativeStreamMiddleware struct {
	middleware.BaseMiddleware
	repeat bool
}

func (*nativeStreamMiddleware) Name() string { return "native_stream" }
func (m *nativeStreamMiddleware) ToolCallMiddlewares() []compose.ToolMiddleware {
	return []compose.ToolMiddleware{{Streamable: func(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
		return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
			copy := *input
			copy.Arguments = "modified"
			out, err := next(ctx, &copy)
			if err != nil {
				return nil, err
			}
			if m.repeat {
				return next(ctx, &copy)
			}
			return &compose.StreamToolOutput{Result: schema.StreamReaderWithConvert(out.Result, func(s string) (string, error) { return strings.ToUpper(s), nil })}, nil
		}
	}}}
}
func TestRun_NativeStreamMiddlewareTransformsChunksAfterPolicy(t *testing.T) {
	for _, deny := range []bool{false, true} {
		tool := &nativeStreamTool{}
		m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "stream_native", Arguments: "original"}}})}}}
		var chunks string
		a, err := New(context.Background(), WithConfig(&Config{Model: m, ToolDescriptors: []tools.Descriptor{{Tool: tool, ReturnDirect: true}}, Middlewares: []middleware.Middleware{&nativeStreamMiddleware{}}, Policy: tools.PolicyFunc(func(_ context.Context, call types.ToolCall, _ tools.Descriptor) (tools.Decision, error) {
			if call.Arguments != "modified" {
				t.Fatal("policy bypassed rewritten arguments")
			}
			if deny {
				return tools.Decision{Action: tools.Deny, Reason: "denied"}, nil
			}
			return tools.Decision{Action: tools.Allow}, nil
		}), Emit: func(_ context.Context, event types.RuntimeEvent) error {
			if chunk, ok := event.Data.(types.ToolOutputChunk); ok {
				chunks += chunk.Content
			}
			return nil
		}}))
		if err != nil {
			t.Fatal(err)
		}
		out, err := a.Run(context.Background(), []*schema.Message{schema.UserMessage("go")})
		if err != nil {
			t.Fatal(err)
		}
		want, calls := "ONETWO", 1
		if deny {
			want, calls = "DENIED", 0
		}
		if out.Content != want || chunks != want || tool.calls != calls || m.calls != 1 {
			t.Fatalf("out=%v chunks=%q calls=%d model=%d", out, chunks, tool.calls, m.calls)
		}
		if !deny && tool.arguments != "modified" {
			t.Fatal("tool arguments not transformed")
		}
	}
}
func TestRun_NativeStreamMiddlewareCannotOpenToolTwice(t *testing.T) {
	tool := &nativeStreamTool{}
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "stream_native", Arguments: "original"}}})}}}
	a, err := New(context.Background(), WithConfig(&Config{Model: m, ToolDescriptors: []tools.Descriptor{{Tool: tool}}, Middlewares: []middleware.Middleware{&nativeStreamMiddleware{repeat: true}}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(context.Background(), []*schema.Message{schema.UserMessage("go")})
	if err == nil || !strings.Contains(err.Error(), "more than once") || tool.calls != 1 || m.calls != 1 {
		t.Fatalf("err=%v tool=%d model=%d", err, tool.calls, m.calls)
	}
}

func (m *nativeToolMiddleware) Name() string { return m.name }
func (m *nativeToolMiddleware) ToolCallMiddlewares() []compose.ToolMiddleware {
	return []compose.ToolMiddleware{{Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
		return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
			copy := *input
			copy.Arguments = m.name + ":" + input.Arguments
			out, err := next(ctx, &copy)
			if err != nil {
				return nil, err
			}
			if m.repeat {
				out, err = next(ctx, &copy)
				if err != nil {
					return nil, err
				}
			}
			return &compose.ToolOutput{Result: m.name + ":" + out.Result}, nil
		}
	}}}
}

func TestRun_NativeInvokableMiddlewarePreservesPolicyAndResult(t *testing.T) {
	for _, deny := range []bool{false, true} {
		tool := &countingTool{}
		m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "args"}}})}}}
		decisions := 0
		a, err := New(context.Background(), WithConfig(&Config{
			Model: m, ToolDescriptors: []tools.Descriptor{{Tool: tool, ReturnDirect: true}},
			Middlewares: []middleware.Middleware{&nativeToolMiddleware{name: "outer", repeat: true}, &nativeToolMiddleware{name: "inner", repeat: true}},
			Policy: tools.PolicyFunc(func(_ context.Context, call types.ToolCall, _ tools.Descriptor) (tools.Decision, error) {
				decisions++
				if call.Arguments != "inner:outer:args" || call.ID != "call" {
					t.Fatalf("policy did not see final arguments: %+v", call)
				}
				if deny {
					return tools.Decision{Action: tools.Deny, Reason: "denied"}, nil
				}
				return tools.Decision{Action: tools.Allow}, nil
			}),
		}))
		if err != nil {
			t.Fatal(err)
		}
		out, err := a.Run(context.Background(), []*schema.Message{schema.UserMessage("go")})
		if err != nil {
			t.Fatal(err)
		}
		want, calls := "outer:inner:inner:outer:args", int32(1)
		if deny {
			want, calls = "outer:inner:denied", 0
		}
		if out.Content != want || tool.count.Load() != calls || decisions != 1 || m.calls != 1 {
			t.Fatalf("out=%v tools=%d decisions=%d model=%d", out, tool.count.Load(), decisions, m.calls)
		}
		if deny && !a.state.Calls[0].Result.IsError {
			t.Fatal("denial lost error flag through middleware")
		}
	}
}
