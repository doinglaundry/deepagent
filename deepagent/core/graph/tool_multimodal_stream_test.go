package graph

import (
	"context"
	"eino-cli/deepagent/core/middleware"
	"errors"
	"github.com/cloudwego/eino/compose"
	"strings"
	"testing"
	"time"

	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type imageStreamTool struct {
	calls     int
	arguments string
	run       func(context.Context) (*schema.StreamReader[*schema.ToolResult], error)
}

func (*imageStreamTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "image_stream"}, nil
}
func (t *imageStreamTool) StreamableRun(ctx context.Context, args *schema.ToolArgument, _ ...einotool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
	t.calls++
	t.arguments = args.Text
	if t.run != nil {
		return t.run(ctx)
	}
	url := "https://example.test/stream.png"
	return schema.StreamReaderFromArray([]*schema.ToolResult{
		{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "first "}}},
		{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "second"}, {Type: schema.ToolPartTypeImage, Image: &schema.ToolOutputImage{MessagePartCommon: schema.MessagePartCommon{URL: &url}}}}},
	}), nil
}
func TestRun_EnhancedStreamPreservesTextOrderAndImage(t *testing.T) {
	tool := &imageStreamTool{}
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "image-call", Function: schema.FunctionCall{Name: "image_stream", Arguments: "{}"}}})}}}
	var chunks []string
	a, err := New(context.Background(), WithConfig(&Config{Model: m, ToolDescriptors: []tools.Descriptor{{Tool: tool, ReturnDirect: true}}, Emit: func(_ context.Context, event types.RuntimeEvent) error {
		if chunk, ok := event.Data.(types.ToolOutputChunk); ok {
			chunks = append(chunks, chunk.Content)
		}
		return nil
	}}))
	if err != nil {
		t.Fatal(err)
	}
	output, err := a.Run(context.Background(), []*schema.Message{schema.UserMessage("show")})
	if err != nil {
		t.Fatal(err)
	}
	if tool.calls != 1 || m.calls != 1 || output.Content != "first second" || len(output.UserInputMultiContent) != 2 || *output.UserInputMultiContent[1].Image.URL != "https://example.test/stream.png" {
		t.Fatalf("tool=%d model=%d output=%+v", tool.calls, m.calls, output)
	}
	if len(chunks) != 2 || chunks[0] != "first " || chunks[1] != "second" {
		t.Fatalf("chunks=%v", chunks)
	}
}

type enhancedStreamMiddleware struct {
	middleware.BaseMiddleware
	repeat bool
}

func (*enhancedStreamMiddleware) Name() string { return "enhanced_stream" }
func (m *enhancedStreamMiddleware) ToolCallMiddlewares() []compose.ToolMiddleware {
	return []compose.ToolMiddleware{{EnhancedStreamable: func(next compose.EnhancedStreamableToolEndpoint) compose.EnhancedStreamableToolEndpoint {
		return func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedStreamableToolOutput, error) {
			copy := *input
			copy.Arguments = `{"modified":true}`
			out, err := next(ctx, &copy)
			if err != nil {
				return nil, err
			}
			if m.repeat {
				return next(ctx, &copy)
			}
			return &compose.EnhancedStreamableToolOutput{Result: schema.StreamReaderWithConvert(out.Result, func(chunk *schema.ToolResult) (*schema.ToolResult, error) {
				copy := *chunk
				copy.Parts = append([]schema.ToolOutputPart(nil), chunk.Parts...)
				for i := range copy.Parts {
					copy.Parts[i].Text = strings.ToUpper(copy.Parts[i].Text)
				}
				return &copy, nil
			})}, nil
		}
	}}}
}
func enhancedStreamModel() *sequenceModel {
	return &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "image-call", Function: schema.FunctionCall{Name: "image_stream", Arguments: "{}"}}})}}}
}
func TestRun_EnhancedStreamMiddlewarePolicyAndDeduplication(t *testing.T) {
	for _, scenario := range []string{"allow", "deny", "repeat"} {
		t.Run(scenario, func(t *testing.T) {
			tool := &imageStreamTool{}
			m := enhancedStreamModel()
			var chunks string
			a, err := New(context.Background(), WithConfig(&Config{Model: m, ToolDescriptors: []tools.Descriptor{{Tool: tool, ReturnDirect: true}}, Middlewares: []middleware.Middleware{&enhancedStreamMiddleware{repeat: scenario == "repeat"}}, Policy: tools.PolicyFunc(func(_ context.Context, call types.ToolCall, _ tools.Descriptor) (tools.Decision, error) {
				if call.Arguments != `{"modified":true}` {
					t.Errorf("wrong policy arguments: %s", call.Arguments)
				}
				if scenario == "deny" {
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
			output, err := a.Run(context.Background(), []*schema.Message{schema.UserMessage("show")})
			if scenario == "repeat" {
				if err == nil || !strings.Contains(err.Error(), "more than once") || tool.calls != 1 {
					t.Fatalf("err=%v calls=%d", err, tool.calls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want, calls := "FIRST SECOND", 1
			if scenario == "deny" {
				want, calls = "DENIED", 0
			}
			if chunks != want || output.Content != want || tool.calls != calls || m.calls != 1 {
				t.Fatalf("chunks=%s output=%+v tool=%d model=%d", chunks, output, tool.calls, m.calls)
			}
			if scenario == "allow" && (tool.arguments != `{"modified":true}` || len(output.UserInputMultiContent) != 2) {
				t.Fatalf("arguments=%s output=%+v", tool.arguments, output)
			}
		})
	}
}
func TestRun_EnhancedStreamCancelReleasesProducer(t *testing.T) {
	opened, exited := make(chan struct{}), make(chan struct{})
	tool := &imageStreamTool{run: func(ctx context.Context) (*schema.StreamReader[*schema.ToolResult], error) {
		reader, writer := schema.Pipe[*schema.ToolResult](0)
		go func() { defer close(exited); defer writer.Close(); close(opened); <-ctx.Done() }()
		return reader, nil
	}}
	a, err := New(context.Background(), WithConfig(&Config{Model: enhancedStreamModel(), ToolDescriptors: []tools.Descriptor{{Tool: tool}}}))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := a.Run(context.Background(), []*schema.Message{schema.UserMessage("show")})
		done <- err
	}()
	<-opened
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	select {
	case <-exited:
	case <-ctx.Done():
		t.Fatal("producer leaked")
	}
}
func TestRun_EnhancedStreamOpenErrorClosesReturnedReader(t *testing.T) {
	reader, writer := schema.Pipe[*schema.ToolResult](0)
	defer writer.Close()
	want := errors.New("stream open failure")
	tool := &imageStreamTool{run: func(context.Context) (*schema.StreamReader[*schema.ToolResult], error) { return reader, want }}
	a, err := New(context.Background(), WithConfig(&Config{Model: enhancedStreamModel(), ToolDescriptors: []tools.Descriptor{{Tool: tool, ReturnDirect: true}}}))
	if err != nil {
		t.Fatal(err)
	}
	message, err := a.Run(context.Background(), []*schema.Message{schema.UserMessage("show")})
	if err != nil || message.Content != want.Error() {
		t.Fatalf("message=%v err=%v", message, err)
	}
	closed := make(chan bool, 1)
	go func() { closed <- writer.Send(&schema.ToolResult{}, nil) }()
	select {
	case ok := <-closed:
		if !ok {
			t.Fatal("reader left open")
		}
	case <-time.After(time.Second):
		reader.Close()
		t.Fatal("reader not closed")
	}
}
