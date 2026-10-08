package tools

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"eino-cli/deepagent/graph/types"
	messagepkg "eino-cli/deepagent/message"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type childRunFunc func(context.Context, ChildRequest, types.ModelChunkSink) (*messagepkg.Message, error)

func (runChild childRunFunc) Run(ctx context.Context, childRequest ChildRequest, emitChunk types.ModelChunkSink) (*messagepkg.Message, error) {
	return runChild(ctx, childRequest, emitChunk)
}

func TestStreamingTaskConsumerCloseCancelsSilentChild(t *testing.T) {
	childStarted, childStopped := make(chan struct{}), make(chan struct{})
	childRunner := childRunFunc(func(ctx context.Context, _ ChildRequest, _ types.ModelChunkSink) (*messagepkg.Message, error) {
		close(childStarted)
		<-ctx.Done()
		close(childStopped)
		return nil, ctx.Err()
	})
	taskTool := NewStreamingTaskTool(childRunner, false).Tool.(einotool.StreamableTool)
	stream, err := taskTool.StreamableRun(context.Background(), `{"description":"wait"}`)
	if err != nil {
		t.Fatal(err)
	}
	<-childStarted
	stream.Close()
	select {
	case <-childStopped:
	case <-time.After(time.Second):
		t.Fatal("child survived consumer close")
	}
}

func TestStreamingTaskFallbackAndPanic(t *testing.T) {
	for _, shouldPanic := range []bool{false, true} {
		childRunner := childRunFunc(func(context.Context, ChildRequest, types.ModelChunkSink) (*messagepkg.Message, error) {
			if shouldPanic {
				panic("child failure")
			}
			return messagepkg.NewAssistantMessage("final", nil), nil
		})
		stream, err := NewStreamingTaskTool(childRunner, false).Tool.(einotool.StreamableTool).StreamableRun(context.Background(), `{"prompt":"go"}`)
		if err != nil {
			t.Fatal(err)
		}
		chunk, err := stream.Recv()
		if shouldPanic {
			if err == nil || !strings.Contains(err.Error(), "child failure") {
				t.Fatalf("chunk=%q err=%v", chunk, err)
			}
		} else {
			if err != nil || chunk != "final" {
				t.Fatalf("chunk=%q err=%v", chunk, err)
			}
			_, recvErr := stream.Recv()
			if recvErr != io.EOF {
				t.Fatal(recvErr)
			}
		}
		stream.Close()
	}
}

func TestTaskToolNameContract(t *testing.T) {
	testCases := []struct {
		name     string
		names    []string
		args     string
		want     string
		required bool
	}{
		{"custom name required", []string{"custom"}, `{"description":"go"}`, "", true},
		{"explicit custom", []string{"custom"}, `{"subagent_type":"custom","description":"go"}`, "custom", true},
		{"registered default", []string{"custom", "general-purpose"}, `{"description":"go"}`, "general-purpose", false},
		{"standalone default", nil, `{"description":"go"}`, "general-purpose", false},
	}
	for _, testCase := range testCases {
		for _, mode := range []string{"invoke", "stream"} {
			t.Run(testCase.name+"/"+mode, func(t *testing.T) {
				childCalls := 0
				childRunner := childRunFunc(func(_ context.Context, childRequest ChildRequest, _ types.ModelChunkSink) (*messagepkg.Message, error) {
					childCalls++
					return messagepkg.NewAssistantMessage(childRequest.Name, nil), nil
				})
				toolDescriptor := NewTaskTool(childRunner, testCase.names...)
				if mode == "stream" {
					toolDescriptor = NewStreamingTaskTool(childRunner, false, testCase.names...)
				}
				toolInfo, err := toolDescriptor.Tool.Info(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				toolSchema, err := toolInfo.ParamsOneOf.ToJSONSchema()
				if err != nil {
					t.Fatal(err)
				}
				encodedToolSchema, err := json.Marshal(toolSchema)
				if err != nil {
					t.Fatal(err)
				}
				var schemaFields struct {
					Required []string `json:"required"`
				}
				err = json.Unmarshal(encodedToolSchema, &schemaFields)
				if err != nil {
					t.Fatal(err)
				}
				if slices.Contains(schemaFields.Required, "subagent_type") != testCase.required || !slices.Contains(schemaFields.Required, "description") || strings.Contains(toolInfo.Desc, "Omit subagent_type") == testCase.required {
					t.Fatalf("schema=%s description=%q", encodedToolSchema, toolInfo.Desc)
				}
				var output string
				if mode == "invoke" {
					output, err = toolDescriptor.Tool.(einotool.InvokableTool).InvokableRun(context.Background(), testCase.args)
				} else {
					var stream *schema.StreamReader[string]
					stream, err = toolDescriptor.Tool.(einotool.StreamableTool).StreamableRun(context.Background(), testCase.args)
					if err == nil {
						defer stream.Close()
						output, err = stream.Recv()
						if err == nil {
							_, endErr := stream.Recv()
							if endErr != io.EOF {
								t.Fatalf("stream end: %v", endErr)
							}
						}
					}
				}
				if testCase.want == "" {
					if err == nil || !strings.Contains(err.Error(), "subagent_type") || childCalls != 0 {
						t.Fatalf("missing-name result=%q err=%v calls=%d", output, err, childCalls)
					}
				} else if err != nil || output != testCase.want || childCalls != 1 {
					t.Fatalf("result=%q want=%q err=%v calls=%d", output, testCase.want, err, childCalls)
				}
			})
		}
	}
}
