package graph

import (
	"context"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"errors"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"io"
	"testing"
	"time"
)

func TestStreamReservesExecutionBeforeGoroutineStarts(t *testing.T) {
	for range 20 {
		m := &cancelModel{started: make(chan struct{}), stopped: make(chan struct{})}
		a, err := New(context.Background(), WithModel(m))
		if err != nil {
			t.Fatal(err)
		}
		reader, err := a.Stream(context.Background(), []*schema.Message{schema.UserMessage("first")})
		if err != nil {
			t.Fatal(err)
		}
		second, err := a.Stream(context.Background(), []*schema.Message{schema.UserMessage("second")})
		if err == nil || second != nil {
			t.Fatal("second stream stole ownership")
		}
		if _, err := a.Run(context.Background(), []*schema.Message{schema.UserMessage("third")}); err == nil {
			t.Fatal("Run bypassed stream reservation")
		}
		select {
		case <-m.started:
		case <-time.After(time.Second):
			t.Fatal("first stream did not execute")
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if err := a.Close(ctx); err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
		reader.Close()
		select {
		case <-m.stopped:
		default:
			t.Fatal("Close returned before model cleanup")
		}
		a.mu.Lock()
		pending := a.streamDone != nil || a.streamCancel != nil || a.active
		a.mu.Unlock()
		if pending {
			t.Fatal("Close left stream transport active")
		}
	}
}

func TestStreamEOFMakesNextExecutionAvailable(t *testing.T) {
	for range 20 {
		m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("first", nil)}, {schema.AssistantMessage("second", nil)}}}
		a, err := New(context.Background(), WithModel(m))
		if err != nil {
			t.Fatal(err)
		}
		reader, err := a.Stream(context.Background(), []*schema.Message{schema.UserMessage("go")})
		if err != nil {
			t.Fatal(err)
		}
		for {
			_, err := reader.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		reader.Close()
		out, err := a.Run(context.Background(), []*schema.Message{schema.UserMessage("again")})
		if err != nil || out.Content != "second" {
			t.Fatalf("next run unavailable: out=%v err=%v", out, err)
		}
	}
}

func TestCloseWaitsForPendingStreamTransport(t *testing.T) {
	for range 20 {
		a, err := New(context.Background(), WithModel(&sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("done", nil)}}}))
		if err != nil {
			t.Fatal(err)
		}
		reader, err := a.Stream(context.Background(), []*schema.Message{schema.UserMessage("go")})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if err := a.Close(ctx); err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
		reader.Close()
		a.mu.Lock()
		pending := a.streamDone != nil || a.active
		a.mu.Unlock()
		if pending {
			t.Fatal("Close returned with a pending stream")
		}
	}
}

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
	a, err := New(context.Background(), WithConfig(&Config{Model: &sequenceModel{}, Middlewares: []middleware.Middleware{&streamErrorMiddleware{reader: reader, err: want}}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(context.Background(), []*schema.Message{schema.UserMessage("go")})
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
	registry, err := tools.NewRegistry(context.Background(), []tools.Descriptor{{Tool: &streamErrorTool{reader: reader}}})
	if err != nil {
		t.Fatal(err)
	}
	e := newToolExecutor("run", registry, 1, nil)
	result, err := e.execute(context.Background(), types.ToolCall{ID: "call", Name: "stream_error", Arguments: "{}"}, nil, nil)
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
