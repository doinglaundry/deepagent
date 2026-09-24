package graph

import (
	"context"
	"strings"
	"testing"
	"time"

	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type eventStreamTool struct{}

func (*eventStreamTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "stream_tool"}, nil
}

func TestRun_ToolPanicDoesNotHangCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}}}
	a, err := New(ctx, WithConfig(&Config{Model: m, ToolDescriptors: []tools.Descriptor{{Tool: &panicTool{}}}}))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := a.Run(ctx, []*schema.Message{schema.UserMessage("go")}); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "tool crashed") {
			t.Fatalf("err=%v", err)
		}
	case <-ctx.Done():
		t.Fatal("run cleanup hung after tool panic")
	}
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if m.calls != 1 {
		t.Fatal("internal tool failure was swallowed and model called again")
	}
}
func (*eventStreamTool) StreamableRun(context.Context, string, ...einotool.Option) (*schema.StreamReader[string], error) {
	return schema.StreamReaderFromArray([]string{"one", "two"}), nil
}

func TestRun_ModelAndToolStreamsPreserveOrder(t *testing.T) {
	var events []types.RuntimeEvent
	m := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("calling", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "stream_tool", Arguments: "{}"}}})},
		{schema.AssistantMessage("done", nil)},
	}}
	a, err := New(context.Background(), WithConfig(&Config{Model: m, ToolDescriptors: []tools.Descriptor{{Tool: &eventStreamTool{}}}, Emit: func(_ context.Context, e types.RuntimeEvent) error { events = append(events, e); return nil }}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}); err != nil {
		t.Fatal(err)
	}
	start, end, chunks := -1, -1, 0
	for i, e := range events {
		if e.Sequence != uint64(i+1) {
			t.Fatalf("sequence=%d index=%d", e.Sequence, i)
		}
		switch e.Kind {
		case "tool_start":
			if start >= 0 {
				t.Fatal("duplicate start")
			}
			start = i
			state, ok := e.Data.(types.ToolCallState)
			if !ok || state.Call.Name != "stream_tool" || state.Call.Arguments != "{}" || state.StartedAt.IsZero() {
				t.Fatalf("missing start metadata: %+v", e)
			}
		case "tool_call_output_chunk":
			chunk, ok := e.Data.(types.ToolOutputChunk)
			if !ok || chunk.Call.Name != "stream_tool" || chunk.Call.ID != "call" || chunk.Content == "" {
				t.Fatalf("missing chunk metadata: %+v", e)
			}
			if start < 0 || end >= 0 {
				t.Fatal("chunk outside tool lifecycle")
			}
			chunks++
		case "tool_end":
			end = i
			state, ok := e.Data.(types.ToolCallState)
			if !ok || state.Result == nil || state.Result.Content != "onetwo" || state.StartedAt.IsZero() || state.Call.Name != "stream_tool" {
				t.Fatalf("missing end metadata: %+v", e)
			}
		}
	}
	if start < 0 || end <= start || chunks != 2 {
		t.Fatalf("start=%d end=%d chunks=%d", start, end, chunks)
	}
}
