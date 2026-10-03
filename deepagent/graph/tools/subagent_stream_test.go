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
			_, recvErr := stream.Recv()
			if recvErr != io.EOF {
				t.Fatal(recvErr)
			}
		}
		stream.Close()
	}
}

func TestTaskToolNameContract(t *testing.T) {
	cases := []struct {
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
	for _, tc := range cases {
		for _, mode := range []string{"invoke", "stream"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				calls := 0
				runner := childRunFunc(func(_ context.Context, request ChildRequest, _ types.ModelChunkSink) (*schema.Message, error) {
					calls++
					return schema.AssistantMessage(request.Name, nil), nil
				})
				item := NewTaskTool(runner, tc.names...)
				if mode == "stream" {
					item = NewStreamingTaskTool(runner, tc.names...)
				}
				info, err := item.Info(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				shape, err := info.ParamsOneOf.ToJSONSchema()
				if err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(shape)
				if err != nil {
					t.Fatal(err)
				}
				var fields struct {
					Required []string `json:"required"`
				}
				err = json.Unmarshal(raw, &fields)
				if err != nil {
					t.Fatal(err)
				}
				if slices.Contains(fields.Required, "subagent_type") != tc.required || !slices.Contains(fields.Required, "description") || strings.Contains(info.Desc, "Omit subagent_type") == tc.required {
					t.Fatalf("schema=%s description=%q", raw, info.Desc)
				}
				var result string
				if mode == "invoke" {
					result, err = item.(einotool.InvokableTool).InvokableRun(context.Background(), tc.args)
				} else {
					var stream *schema.StreamReader[string]
					stream, err = item.(einotool.StreamableTool).StreamableRun(context.Background(), tc.args)
					if err == nil {
						defer stream.Close()
						result, err = stream.Recv()
						if err == nil {
							_, endErr := stream.Recv()
							if endErr != io.EOF {
								t.Fatalf("stream end: %v", endErr)
							}
						}
					}
				}
				if tc.want == "" {
					if err == nil || !strings.Contains(err.Error(), "subagent_type") || calls != 0 {
						t.Fatalf("missing-name result=%q err=%v calls=%d", result, err, calls)
					}
				} else if err != nil || result != tc.want || calls != 1 {
					t.Fatalf("result=%q want=%q err=%v calls=%d", result, tc.want, err, calls)
				}
			})
		}
	}
}
