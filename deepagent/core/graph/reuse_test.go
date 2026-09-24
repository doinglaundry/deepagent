package graph

import (
	"context"
	"strings"
	"testing"

	"eino-cli/deepagent/core/backend"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/schema"
)

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
	workspace, err := backend.NewLocalFilesystem(&backend.FilesystemBackendConfig{RootDir: root, VirtualMode: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close(context.Background())
	a, err := New(context.Background(), WithConfig(&Config{Model: m, Workspace: workspace, FilesystemConfig: &FilesystemConfig{WorkDir: root, DisableApplyPatch: true}, Policy: tools.PolicyFunc(func(context.Context, types.ToolCall, tools.Descriptor) (tools.Decision, error) {
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
