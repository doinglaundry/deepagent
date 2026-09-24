package tools

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"eino-cli/deepagent/core/types"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type childRunFunc func(context.Context, ChildRequest, types.ModelChunkSink) (*schema.Message, error)

func (f childRunFunc) Run(ctx context.Context, req ChildRequest, emit types.ModelChunkSink) (*schema.Message, error) {
	return f(ctx, req, emit)
}

func TestStreamingTaskConsumerCloseCancelsSilentChild(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	runner := childRunFunc(func(ctx context.Context, _ ChildRequest, _ types.ModelChunkSink) (*schema.Message, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	})
	tool := NewStreamingTaskTool(runner).(einotool.StreamableTool)
	stream, err := tool.StreamableRun(context.Background(), `{"description":"wait"}`)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	stream.Close()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("child survived consumer close")
	}
}
func TestStreamingTaskFallbackAndPanic(t *testing.T) {
	for _, panics := range []bool{false, true} {
		runner := childRunFunc(func(context.Context, ChildRequest, types.ModelChunkSink) (*schema.Message, error) {
			if panics {
				panic("child failure")
			}
			return schema.AssistantMessage("final", nil), nil
		})
		stream, err := NewStreamingTaskTool(runner).(einotool.StreamableTool).StreamableRun(context.Background(), `{"prompt":"go"}`)
		if err != nil {
			t.Fatal(err)
		}
		chunk, err := stream.Recv()
		if panics {
			if err == nil || !strings.Contains(err.Error(), "child failure") {
				t.Fatalf("chunk=%q err=%v", chunk, err)
			}
		} else {
			if err != nil || chunk != "final" {
				t.Fatalf("chunk=%q err=%v", chunk, err)
			}
			if _, err := stream.Recv(); err != io.EOF {
				t.Fatal(err)
			}
		}
		stream.Close()
	}
}
