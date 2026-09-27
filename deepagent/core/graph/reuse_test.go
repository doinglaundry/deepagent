package graph

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"eino-cli/deepagent/core/backend"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type overlappingRunModel struct {
	ready   chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (m *overlappingRunModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (*overlappingRunModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("unexpected non-stream model call")
}
func (m *overlappingRunModel) Stream(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.calls.Add(1)
	for _, message := range input {
		if message.Role == schema.Tool {
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("done", nil)}), nil
		}
	}
	select {
	case m.ready <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-m.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	call := schema.ToolCall{ID: "same-call", Type: "function", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("", []schema.ToolCall{call})}), nil
}

func TestRun_ConcurrentRunsDoNotShareState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	guard := middleware.NewLoopGuard()
	guard.HardLimit = 2
	m := &overlappingRunModel{ready: make(chan struct{}, 2), release: make(chan struct{})}
	tool := &countingTool{}
	cfg := &Config{Model: m, Middlewares: []middleware.Middleware{guard}, ToolDescriptors: []tools.Descriptor{{Tool: tool}}}
	agents := make([]*DeepAgent, 2)
	for i := range agents {
		var err error
		agents[i], err = New(ctx, WithConfig(cfg))
		if err != nil {
			t.Fatal(err)
		}
		defer agents[i].Close(context.Background())
	}
	if agents[0].middlewares[0] == agents[1].middlewares[0] {
		t.Fatal("runs share mutable LoopGuard")
	}
	results := make(chan error, 2)
	for _, agent := range agents {
		go func(a *DeepAgent) {
			out, err := a.Run(ctx, []*schema.Message{schema.UserMessage("go")})
			if err == nil && (out == nil || out.Content != "done") {
				err = fmt.Errorf("unexpected output: %v", out)
			}
			results <- err
		}(agent)
	}
	for range agents {
		select {
		case <-m.ready:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	close(m.release)
	for range agents {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if got := tool.count.Load(); got != 2 {
		t.Fatalf("shared middleware suppressed a tool call: %d", got)
	}
}

func TestRun_ReusingAgentRecreatesMutableMiddleware(t *testing.T) {
	guard := middleware.NewLoopGuard()
	guard.HardLimit = 2
	m := &sequenceModel{}
	for range 2 {
		m.responses = append(m.responses,
			[]*schema.Message{schema.AssistantMessage("", []schema.ToolCall{{ID: "first", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})},
			[]*schema.Message{schema.AssistantMessage("stopping loop", []schema.ToolCall{{ID: "second", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})})
	}
	tool := &countingTool{}
	a, err := New(context.Background(), WithConfig(&Config{Model: m, Middlewares: []middleware.Middleware{guard}, ToolDescriptors: []tools.Descriptor{{Tool: tool}}}))
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		out, err := a.Run(context.Background(), []*schema.Message{schema.UserMessage("go")})
		if err != nil {
			t.Fatal(err)
		}
		if out.Content != "stopping loop" || int(tool.count.Load()) != i || m.calls != i*2 {
			t.Fatalf("run=%d output=%v tool=%d model=%d", i, out, tool.count.Load(), m.calls)
		}
	}
	if len(a.conversation.History(context.Background())) != 8 {
		t.Fatal("reinitializing a run lost conversation history")
	}
}

func TestRun_ReusingAgentRebindsCommandTools(t *testing.T) {
	m := &sequenceModel{}
	for range 2 {
		m.responses = append(m.responses,
			[]*schema.Message{schema.AssistantMessage("", []schema.ToolCall{{ID: "command", Function: schema.FunctionCall{Name: "execute", Arguments: `{"command":"pwd"}`}}})},
			[]*schema.Message{schema.AssistantMessage("done", nil)})
	}
	root := t.TempDir()
	filesystem, err := backend.NewLocalFilesystem(&backend.LocalFilesystemConfig{RootDir: root, VirtualMode: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer filesystem.Close(context.Background())
	a, err := New(context.Background(), WithConfig(&Config{Model: m, Filesystem: filesystem, FilesystemConfig: &FilesystemConfig{DisableApplyPatch: true}, Policy: tools.PolicyFunc(func(context.Context, types.ToolCall, tools.Descriptor) (tools.Decision, error) {
		return tools.Decision{Action: tools.Allow}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := a.Run(context.Background(), []*schema.Message{schema.UserMessage("where")}); err != nil {
			t.Fatal(err)
		}
		history := a.conversation.History(context.Background())
		result := history[len(history)-2]
		if result.Role != schema.Tool || !strings.Contains(result.Content, "exit_code=0") || !strings.Contains(result.Content, root) {
			t.Fatalf("run=%d result=%+v", i, result)
		}
	}
}
