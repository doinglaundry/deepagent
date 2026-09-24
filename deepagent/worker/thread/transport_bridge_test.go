package thread

import (
	"context"
	"testing"
	"time"

	checkpoints "eino-cli/deepagent/core/checkpoint"
	"eino-cli/deepagent/core/graph"
	"eino-cli/deepagent/core/runtime/agentthread"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/manager/api"
	"eino-cli/deepagent/protocol"
	corethread "eino-cli/deepagent/thread"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type bridgeModel struct{ approval bool }

func (m *bridgeModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (*bridgeModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	panic("must stream")
}
func (m *bridgeModel) Stream(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message := schema.AssistantMessage("done", nil)
	if m.approval && !hasBridgeToolResult(messages) {
		message = schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "write", Arguments: "{}"}}})
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

type bridgeTool struct{}

func (bridgeTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "write"}, nil
}
func (bridgeTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	panic("unapproved tool executed")
}
func TestManagedOutputFromActualCanonicalThread(t *testing.T) {
	for _, approval := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "blocked"}[approval], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			store, err := checkpoints.NewFile(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			bus := make(chan agentthread.Event, 32)
			core := agentthread.New("thread", &agentthread.RunConfig{Agent: graph.Config{Model: &bridgeModel{approval: approval}, DisableSubAgent: true, CheckpointStore: store, ToolDescriptors: []tools.Descriptor{{Tool: bridgeTool{}, RequiresApproval: true}}}}, bus, agentthread.ThreadOptions{})
			adapter, err := corethread.NewThread(corethread.AdapterConfig{ThreadID: "thread", SessionID: "session", Thread: core, EventBus: bus})
			if err != nil {
				t.Fatal(err)
			}
			output, err := adapter.Init(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer adapter.Close(context.Background())
			owner := api.Thread{ID: "thread", SessionID: "session"}
			input, err := transportInput(protocol.Input{ID: "input", Kind: protocol.InputUser, Text: "go"}, owner)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = adapter.PostMessage(ctx, input); err != nil {
				t.Fatal(err)
			}
			bridge := managedOutput{owner: owner}
			sawStart, sawText := false, false
			for {
				select {
				case item, ok := <-output.Items:
					if !ok {
						t.Fatal("output closed before terminal")
					}
					events, err := bridge.convert(item)
					if err != nil {
						t.Fatal(err)
					}
					for _, event := range events {
						if event.Kind == protocol.EventRunStarted {
							sawStart = true
						}
						if event.Kind == protocol.EventText && event.Text == "done" {
							sawText = true
						}
						if event.Terminal() {
							if item.Yield == nil || item.Yield.Reason != "finished" {
								t.Fatal("released before final yield")
							}
							if !sawStart || len(event.MessageIDs) != 1 || event.MessageIDs[0] != "input" {
								t.Fatalf("lost input identity: %+v", event)
							}
							if approval {
								if event.Kind != protocol.EventBlocked || event.Block == nil || event.Block.ToolName != "write" || event.Block.InterruptID == "" {
									t.Fatalf("block=%+v", event)
								}
							} else if event.Kind != protocol.EventRunCompleted || !sawText {
								t.Fatalf("completion=%+v", event)
							}
							return
						}
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
		})
	}
}

func hasBridgeToolResult(messages []*schema.Message) bool {
	for _, m := range messages {
		if m.Role == schema.Tool {
			return true
		}
	}
	return false
}
