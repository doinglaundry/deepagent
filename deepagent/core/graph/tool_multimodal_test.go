package graph

import (
	"context"

	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"encoding/json"
	"errors"
	einotool "github.com/cloudwego/eino/components/tool"

	"github.com/cloudwego/eino/schema"

	"testing"
	"time"
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
	a, err := New(context.Background(), WithConfig(&Config{Model: m, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool}}, Emit: func(_ context.Context, event types.RuntimeEvent) error {
		if event.Kind == "tool_end" {
			payload := event.Data.(types.ToolEndPayload)
			completed = types.ToolCallState{Call: types.ToolCall{ID: payload.CallID, Name: payload.Name, Arguments: payload.ArgumentsInJSON}, StartedAt: payload.ToolStartTime, Result: &types.ToolResult{CallID: payload.CallID, Content: payload.Result, MultiContent: payload.MultiContent}}
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

func TestRun_EnhancedToolPolicyAndReturnDirect(t *testing.T) {
	for _, deny := range []bool{false, true} {
		tool := &imageTool{}
		m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "image-call", Function: schema.FunctionCall{Name: "image", Arguments: "{}"}}})}}}
		policies := 0
		a, err := New(context.Background(), WithConfig(&Config{Model: m, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool, ReturnDirect: true}}, Policy: tools.PolicyFunc(func(_ context.Context, call types.ToolCall, _ tools.ToolDescriptor) (tools.Decision, error) {
			policies++
			if call.Arguments != `{}` {
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
		want, calls, parts := "image description", 1, 2
		if deny {
			want, calls, parts = "denied", 0, 0
		}
		if tool.calls != calls || m.calls != 1 || policies != 1 || message.Content != want || len(message.UserInputMultiContent) != parts {
			t.Fatalf("tool=%d model=%d policy=%d message=%+v", tool.calls, m.calls, policies, message)
		}
		history := a.conversation.History(context.Background())
		if history[len(history)-1].Role != schema.Tool {
			t.Fatal("ReturnDirect mutated tool history")
		}
	}
}

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
	a, err := New(context.Background(), WithConfig(&Config{Model: m, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool, ReturnDirect: true}}, Emit: func(_ context.Context, event types.RuntimeEvent) error {
		chunk, ok := event.Data.(types.ToolCallOutputChunkPayload)
		if ok {
			chunks = append(chunks, chunk.Chunk)
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

func enhancedStreamModel() *sequenceModel {
	return &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "image-call", Function: schema.FunctionCall{Name: "image_stream", Arguments: "{}"}}})}}}
}
func TestRun_EnhancedStreamPolicyAndReturnDirect(t *testing.T) {
	for _, scenario := range []string{"allow", "deny"} {
		t.Run(scenario, func(t *testing.T) {
			tool := &imageStreamTool{}
			m := enhancedStreamModel()
			var chunks string
			a, err := New(context.Background(), WithConfig(&Config{Model: m, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool, ReturnDirect: true}}, Policy: tools.PolicyFunc(func(_ context.Context, call types.ToolCall, _ tools.ToolDescriptor) (tools.Decision, error) {
				if call.Arguments != `{}` {
					t.Errorf("wrong policy arguments: %s", call.Arguments)
				}
				if scenario == "deny" {
					return tools.Decision{Action: tools.Deny, Reason: "denied"}, nil
				}
				return tools.Decision{Action: tools.Allow}, nil
			}), Emit: func(_ context.Context, event types.RuntimeEvent) error {
				chunk, ok := event.Data.(types.ToolCallOutputChunkPayload)
				if ok {
					chunks += chunk.Chunk
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
			want, calls := "first second", 1
			wantChunks := want
			if scenario == "deny" {
				want, calls = "denied", 0
				wantChunks = ""
			}
			if chunks != wantChunks || output.Content != want || tool.calls != calls || m.calls != 1 {
				t.Fatalf("chunks=%s output=%+v tool=%d model=%d", chunks, output, tool.calls, m.calls)
			}
			if scenario == "allow" && (tool.arguments != `{}` || len(output.UserInputMultiContent) != 2) {
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
	a, err := New(context.Background(), WithConfig(&Config{Model: enhancedStreamModel(), ToolDescriptors: []tools.ToolDescriptor{{Tool: tool}}}))
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
	a, err := New(context.Background(), WithConfig(&Config{Model: enhancedStreamModel(), ToolDescriptors: []tools.ToolDescriptor{{Tool: tool, ReturnDirect: true}}}))
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
