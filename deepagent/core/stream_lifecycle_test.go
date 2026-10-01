package deepagents

import (
	"context"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"errors"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"testing"
	"time"
)

type streamErrorMiddleware struct {
	middleware.BaseMiddleware
	reader *schema.StreamReader[*schema.Message]
	err    error
}

func (*streamErrorMiddleware) Name() string { return "stream_open_error" }
func (m *streamErrorMiddleware) WrapModel(middleware.ModelHandler) middleware.ModelHandler {
	return func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		return m.reader, m.err
	}
}

func TestRun_ModelOpenErrorClosesReturnedStream(t *testing.T) {
	reader, writer := schema.Pipe[*schema.Message](0)
	defer writer.Close()
	want := errors.New("model opening failed")
	a, err := NewRun(context.Background(), WithConfig(&Config{Model: &sequenceModel{}, Middlewares: []middleware.Middleware{&streamErrorMiddleware{reader: reader, err: want}}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Execute(context.Background(), []*schema.Message{schema.UserMessage("go")})
	if !errors.Is(err, want) {
		t.Fatal(err)
	}
	closed := make(chan bool, 1)
	go func() { closed <- writer.Send(nil, nil) }()
	select {
	case ok := <-closed:
		if !ok {
			t.Fatal("stream remains open")
		}
	case <-time.After(time.Second):
		reader.Close()
		t.Fatal("stream was not closed")
	}
}

type streamErrorTool struct{ reader *schema.StreamReader[string] }

func (*streamErrorTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "stream_error"}, nil
}
func (s *streamErrorTool) StreamableRun(context.Context, string, ...einotool.Option) (*schema.StreamReader[string], error) {
	return s.reader, errors.New("tool opening failed")
}

func TestToolExecutor_OpenErrorClosesReturnedStream(t *testing.T) {
	reader, writer := schema.Pipe[string](0)
	defer writer.Close()
	toolSet, err := tools.NewToolSet(context.Background(), []tools.ToolDescriptor{{Tool: &streamErrorTool{reader: reader}}})
	if err != nil {
		t.Fatal(err)
	}
	e := newToolExecutor("run", toolSet, 1, nil)
	result, err := e.execute(context.Background(), types.ToolCall{ID: "call", Name: "stream_error", Arguments: "{}"}, nil)
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("result=%v err=%v", result, err)
	}
	closed := make(chan bool, 1)
	go func() { closed <- writer.Send("", nil) }()
	select {
	case ok := <-closed:
		if !ok {
			t.Fatal("stream remains open")
		}
	case <-time.After(time.Second):
		reader.Close()
		t.Fatal("stream was not closed")
	}
}
