package execution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	filesystempkg "eino-cli/deepagent/graph/filesystem"
	"eino-cli/deepagent/graph/tools"
	"eino-cli/deepagent/graph/types"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestRun_ModelToolModel(t *testing.T) {
	ctx := context.Background()
	call := schema.ToolCall{ID: "call", Type: "function", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}
	chatModel := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{call})}, {schema.AssistantMessage("done", nil)}}}
	tool := &countingTool{}
	graph, err := New(ctx, WithConfig(&Config{Model: chatModel, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool}}}))
	if err != nil {
		t.Fatal(err)
	}
	out, err := graph.Invoke(ctx, []*schema.Message{schema.UserMessage("go")})
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "done" || chatModel.calls != 2 || tool.count.Load() != 1 {
		t.Fatalf("out=%v calls=%d tool=%d", out, chatModel.calls, tool.count.Load())
	}
	if len(chatModel.inputs[1]) != 3 || chatModel.inputs[1][2].Role != schema.Tool || chatModel.inputs[1][2].ToolCallID != "call" {
		t.Fatalf("tool result not in next context: %v", chatModel.inputs[1])
	}
}

func TestRun_ReturnDirectDoesNotCallModelAgain(t *testing.T) {
	ctx := context.Background()
	chatModel := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Type: "function", Function: schema.FunctionCall{Name: "counter", Arguments: "direct"}}})}}}
	graph, err := New(ctx, WithConfig(&Config{Model: chatModel, ToolDescriptors: []tools.ToolDescriptor{{Tool: &countingTool{}, ReturnDirect: true}}}))
	if err != nil {
		t.Fatal(err)
	}
	out, err := graph.Invoke(ctx, []*schema.Message{schema.UserMessage("go")})
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "direct" || chatModel.calls != 1 {
		t.Fatalf("result=%v modelcalls=%d", out, chatModel.calls)
	}
}

func TestRun_ApprovalDenyNeverExecutesTool(t *testing.T) {
	ctx := context.Background()
	store := &checkpointMemory{}
	tool := &countingTool{}
	model := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "approved-call", Type: "function", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}, {schema.AssistantMessage("denied acknowledged", nil)}}}
	config := Config{Model: model, RunID: "run", ThreadID: "thread", CheckpointStore: store, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool, RequiresApproval: true}}}
	first, err := New(ctx, WithConfig(&config))
	if err != nil {
		t.Fatal(err)
	}
	_, err = first.Invoke(ctx, []*schema.Message{schema.UserMessage("do it")}, WithCheckpointID("checkpoint"))
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok {
		t.Fatalf("expected checkpointed interruption, got %v", err)
	}
	if len(info.InterruptContexts) != 1 {
		t.Fatalf("interrupt contexts: %+v", info)
	}
	approval, ok := info.InterruptContexts[0].Info.(*tools.ApprovalInfo)
	if !ok || approval.CallID != "approved-call" {
		t.Fatalf("approval lost tool identity: %+v", info.InterruptContexts[0].Info)
	}
	config.Conversation = first.conversation
	restored, err := New(ctx, WithConfig(&config))
	if err != nil {
		t.Fatal(err)
	}
	reason := "do not change this file"
	out, err := restored.Invoke(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "approved-call", Approved: false, DisapproveReason: &reason}}))
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "denied acknowledged" || tool.count.Load() != 0 || model.calls != 2 {
		t.Fatalf("out=%v tool=%d model=%d", out, tool.count.Load(), model.calls)
	}
	messages := model.inputs[1]
	last := messages[len(messages)-1]
	if last.Role != schema.Tool || last.Content != reason {
		t.Fatalf("model lost rejection reason: %+v", last)
	}
	if approval.Arguments != "{}" {
		t.Fatalf("approval lost arguments: %+v", approval)
	}

}

func TestRun_FilesystemWriteRequiresApprovalWithoutExplicitPolicy(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := &checkpointMemory{}
	model := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "write", Type: "function", Function: schema.FunctionCall{Name: "write_file", Arguments: `{"path":"result.txt","content":"approved"}`}}})},
		{schema.AssistantMessage("done", nil)},
	}}
	config := Config{ThreadID: "thread", RunID: "run", Model: model, CheckpointStore: store,
		Filesystem:       newTestLocalFilesystem(t, &filesystempkg.LocalFilesystemConfig{RootDir: root, VirtualMode: true}),
		FilesystemConfig: &FilesystemConfig{DisableExecute: true, DisableApplyPatch: true}}
	first, err := New(ctx, WithConfig(&config))
	if err != nil {
		t.Fatal(err)
	}
	_, err = first.Invoke(ctx, []*schema.Message{schema.UserMessage("write")}, WithCheckpointID("checkpoint"))
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok || len(info.InterruptContexts) != 1 {
		descriptor, found := first.toolSet.GetToolDescriptor("write_file")
		t.Fatalf("expected approval interrupt, got %+v: %v; tool found=%v requires_approval=%v history=%v", info, err, found, descriptor.RequiresApproval, first.conversation.GetHistory(ctx))
	}
	_, statErr := os.Stat(filepath.Join(root, "result.txt"))
	if !os.IsNotExist(statErr) {
		t.Fatalf("write ran before approval: %v", statErr)
	}
	config.Conversation = first.conversation
	restored, err := New(ctx, WithConfig(&config))
	if err != nil {
		t.Fatal(err)
	}
	_, err = restored.Invoke(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{
		info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "write", Approved: true},
	}))
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "result.txt"))
	if err != nil || string(content) != "approved" {
		t.Fatalf("write after approval: %q, %v", content, err)
	}
	if model.calls != 2 {
		t.Fatalf("model calls = %d, want 2", model.calls)
	}
}

func TestCheckpoint_SaveFailureDoesNotPublishBlocked(t *testing.T) {
	ctx := context.Background()
	chatModel := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Type: "function", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}}}
	graph, err := New(ctx, WithConfig(&Config{Model: chatModel, CheckpointStore: &checkpointMemory{fail: true}, ToolDescriptors: []tools.ToolDescriptor{{Tool: &countingTool{}, RequiresApproval: true}}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = graph.Invoke(ctx, []*schema.Message{schema.UserMessage("input")}, WithCheckpointID("checkpoint"))
	if err == nil {
		t.Fatal("checkpoint failure swallowed")
	}
	_, ok := compose.ExtractInterruptInfo(err)
	if ok {
		t.Fatal("failed checkpoint published as resumable interruption")
	}
	if graph.runState.Phase != types.PhaseFailed {
		t.Fatalf("failed checkpoint left phase %s", graph.runState.Phase)
	}
	if chatModel.calls != 0 {
		t.Fatal("model ran before the initial checkpoint was durable")
	}
}

func TestRun_ResumeDoesNotRepeatCompletedTool(t *testing.T) {
	ctx := context.Background()
	store := &checkpointMemory{}
	firstTool := &namedCountingTool{name: "first"}
	approvalTool := &namedCountingTool{name: "approval"}
	chatModel := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "1", Type: "function", Function: schema.FunctionCall{Name: "first", Arguments: "{}"}}, {ID: "2", Type: "function", Function: schema.FunctionCall{Name: "approval", Arguments: "{}"}}})}, {schema.AssistantMessage("done", nil)}}}
	config := Config{Model: chatModel, RunID: "run", CheckpointStore: store, ToolDescriptors: []tools.ToolDescriptor{{Tool: firstTool}, {Tool: approvalTool, RequiresApproval: true}}}
	graph, err := New(ctx, WithConfig(&config))
	if err != nil {
		t.Fatal(err)
	}
	_, err = graph.Invoke(ctx, []*schema.Message{schema.UserMessage("input")}, WithCheckpointID("checkpoint"))
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok {
		t.Fatal(err)
	}
	if firstTool.count != 1 || approvalTool.count != 0 {
		t.Fatal("wrong pre-interrupt side effects")
	}
	config.Conversation = graph.conversation
	restored, err := New(ctx, WithConfig(&config))
	if err != nil {
		t.Fatal(err)
	}
	_, err = restored.Invoke(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{Approved: true}}))
	if err != nil {
		t.Fatal(err)
	}
	if firstTool.count != 1 || approvalTool.count != 1 {
		t.Fatalf("replayed tools first=%d approved=%d", firstTool.count, approvalTool.count)
	}
	replay, err := New(ctx, WithConfig(&config))
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close(ctx)
	_, err = replay.Invoke(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{Approved: true}}))
	if err == nil || firstTool.count != 1 || approvalTool.count != 1 || chatModel.calls != 2 {
		t.Fatalf("completed checkpoint replayed: err=%v first=%d approved=%d model=%d", err, firstTool.count, approvalTool.count, chatModel.calls)
	}
}

func TestRun_EagerExecutesBeforeModelStreamEnds(t *testing.T) {
	for _, withPolicy := range []bool{false, true} {
		t.Run(fmt.Sprint(withPolicy), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			tool := &countingTool{started: make(chan struct{})}
			chatModel := &eagerModel{toolStarted: tool.started}
			starts, policyCalls := 0, 0
			config := &Config{Model: chatModel, EnableEagerTools: true, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool, ParallelSafe: true}}, Emit: func(_ context.Context, e types.RuntimeEvent) error {
				if e.Kind == "tool_start" {
					starts++
				}
				return nil
			}}
			if withPolicy {
				config.Policy = tools.PolicyFunc(func(context.Context, types.ToolCall, tools.ToolDescriptor) (tools.Decision, error) {
					policyCalls++
					return tools.Decision{Action: tools.Allow}, nil
				})
			}
			graph, err := New(ctx, WithConfig(config))
			if err != nil {
				t.Fatal(err)
			}
			result, err := graph.Invoke(ctx, []*schema.Message{schema.UserMessage("go")})
			if err != nil {
				t.Fatal(err)
			}
			if result.Content != "done" || tool.count.Load() != 1 || starts != 1 || policyCalls != map[bool]int{false: 0, true: 1}[withPolicy] {
				t.Fatalf("result=%v count=%d starts=%d policy=%d", result, tool.count.Load(), starts, policyCalls)
			}
		})
	}
}

func TestRun_EmitsTokensAndReturnsFinalMessage(t *testing.T) {
	ctx := context.Background()
	chatModel := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("hel", nil), schema.AssistantMessage("lo", nil)}}}
	var text string
	graph, err := New(ctx, WithConfig(&Config{
		Model: chatModel,
		Emit: func(_ context.Context, event types.RuntimeEvent) error {
			if event.Kind == "llm_token" {
				text += event.Data.(types.LLMTokenChunk).Text
			}
			return nil
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close(ctx)
	result, err := graph.Invoke(ctx, []*schema.Message{schema.UserMessage("go")})
	if err != nil {
		t.Fatal(err)
	}
	if text != "hello" || result.Content != text || chatModel.calls != 1 {
		t.Fatalf("tokens=%q result=%v calls=%d", text, result, chatModel.calls)
	}
	if len(graph.conversation.GetHistory(ctx)) != 2 {
		t.Fatal("missing input or assistant history")
	}
}

func TestRun_CancelAndAgentCloseReleaseResources(t *testing.T) {
	for _, closeAgent := range []bool{false, true} {
		t.Run(fmt.Sprint(closeAgent), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			chatModel := &cancelModel{started: make(chan struct{}), stopped: make(chan struct{})}
			graph, err := New(ctx, WithModel(chatModel))
			if err != nil {
				t.Fatal(err)
			}
			finished := make(chan error, 1)
			go func() {
				_, runErr := graph.Invoke(ctx, []*schema.Message{schema.UserMessage("wait")})
				finished <- runErr
			}()
			select {
			case <-chatModel.started:
			case <-time.After(time.Second):
				t.Fatal("model did not start")
			}
			cleanup, cancelCleanup := context.WithTimeout(context.Background(), time.Second)
			defer cancelCleanup()
			if closeAgent {
				err = graph.Close(cleanup)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			select {
			case err = <-finished:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
			case <-cleanup.Done():
				t.Fatal("run did not stop")
			}
			select {
			case <-chatModel.stopped:
			default:
				t.Fatal("Run returned before model stopped")
			}
			err = graph.Close(cleanup)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGraph_InterruptBeforeInvokeDoesNotClaimExecution(t *testing.T) {
	ctx := context.Background()
	model := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("answer", nil)}}}
	compiledGraph, err := New(ctx, WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	if compiledGraph.Interrupt() {
		t.Fatal("unstarted graph claimed an active interruption")
	}
	result, err := compiledGraph.Invoke(ctx, []*schema.Message{schema.UserMessage("go")})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || len(model.inputs) != 1 {
		t.Fatal("pre-invoke interrupt changed normal execution")
	}
}
