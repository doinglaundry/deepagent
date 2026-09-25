package graph

import (
	"context"
	"eino-cli/deepagent/core/internal/conversation"
	"eino-cli/deepagent/core/tools"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestCheckpoint_LegacyFixturesResume(t *testing.T) {
	raw, err := os.ReadFile("../runtime/checkpointer/testdata/legacy_before_model.json")
	if err != nil {
		t.Fatal(err)
	}
	store := &checkpointMemory{values: map[string][]byte{"fixture": raw}}
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("resumed", nil)}}}
	a, err := New(context.Background(), WithConfig(&Config{Model: m, ThreadID: "thread", RunID: "run", CheckpointStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.Run(context.Background(), nil, WithCheckpointID("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "resumed" || m.calls != 1 || len(m.inputs[0]) != 1 || m.inputs[0][0].Content != "legacy question" {
		t.Fatalf("result=%v calls=%d inputs=%v", result, m.calls, m.inputs)
	}
}

func TestCheckpoint_LegacyToolsRequireMatchingHistory(t *testing.T) {
	raw, err := os.ReadFile("../runtime/checkpointer/testdata/legacy_before_tools.json")
	if err != nil {
		t.Fatal(err)
	}
	tool := &countingTool{}
	m := &sequenceModel{}
	store := &checkpointMemory{values: map[string][]byte{"fixture": raw}}
	a, err := New(context.Background(), WithConfig(&Config{Model: m, ThreadID: "thread", RunID: "run", CheckpointStore: store, ToolDescriptors: []tools.Descriptor{{Tool: tool}}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(context.Background(), nil, WithCheckpointID("fixture"))
	if err == nil || !strings.Contains(err.Error(), "durable conversation history") || tool.count.Load() != 0 || m.calls != 0 {
		t.Fatalf("err=%v tool=%d model=%d", err, tool.count.Load(), m.calls)
	}
	defer a.Close(context.Background())
	ctx := context.Background()
	history := conversation.New("thread", nil, nil, nil)
	if err := history.AddHistory(ctx, "run", schema.UserMessage("legacy question"), schema.AssistantMessage("", []schema.ToolCall{{ID: "legacy-call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})); err != nil {
		t.Fatal(err)
	}
	m.responses = [][]*schema.Message{{schema.AssistantMessage("resumed", nil)}}
	retry, err := New(ctx, WithConfig(&Config{Model: m, ThreadID: "thread", RunID: "run", Conversation: history, CheckpointStore: store, ToolDescriptors: []tools.Descriptor{{Tool: tool}}}))
	if err != nil {
		t.Fatal(err)
	}
	defer retry.Close(ctx)
	if _, err := retry.Run(ctx, nil, WithCheckpointID("fixture")); err != nil {
		t.Fatalf("corrected tool boundary cannot resume: %v", err)
	}
	if tool.count.Load() != 1 || m.calls != 1 {
		t.Fatalf("tools=%d models=%d", tool.count.Load(), m.calls)
	}
}

func TestCheckpoint_LegacyToolBoundaryResumesWithoutRepeatingModel(t *testing.T) {
	ctx := context.Background()
	raw, err := os.ReadFile("../runtime/checkpointer/testdata/legacy_before_tools.json")
	if err != nil {
		t.Fatal(err)
	}
	sidecar, err := os.ReadFile("../runtime/checkpointer/testdata/legacy_before_tools_state.json")
	if err != nil {
		t.Fatal(err)
	}
	store := &checkpointMemory{values: map[string][]byte{"fixture": raw, "deepagent_graph_state_:fixture": sidecar}}
	history := conversation.New("thread", nil, nil, nil)
	if err := history.AddHistory(ctx, "run", schema.UserMessage("legacy question"), schema.AssistantMessage("", []schema.ToolCall{{ID: "legacy-call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})); err != nil {
		t.Fatal(err)
	}
	tool := &countingTool{}
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("resumed", nil)}}}
	a, err := New(ctx, WithConfig(&Config{Model: m, ThreadID: "thread", RunID: "run", MaxModelCalls: 2, Conversation: history, CheckpointStore: store, ToolDescriptors: []tools.Descriptor{{Tool: tool}}}))
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.Run(ctx, nil, WithCheckpointID("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "resumed" || tool.count.Load() != 1 || m.calls != 1 || a.state.ModelCalls != 2 {
		t.Fatalf("out=%v tool=%d model=%d budget=%d", out, tool.count.Load(), m.calls, a.state.ModelCalls)
	}
	if len(m.inputs[0]) != 3 || m.inputs[0][2].ToolCallID != "legacy-call" {
		t.Fatalf("history=%v", m.inputs[0])
	}
}

func TestCheckpoint_LegacyContextHistoryUsesConversation(t *testing.T) {
	for _, mode := range []string{"matching", "missing", "different", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			raw, err := os.ReadFile("../runtime/checkpointer/testdata/legacy_before_tools.json")
			if err != nil {
				t.Fatal(err)
			}
			messages := []*schema.Message{schema.UserMessage("legacy question"), schema.AssistantMessage("", []schema.ToolCall{{ID: "legacy-call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}
			encoded, err := json.Marshal(messages)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "invalid" {
				encoded = []byte("broken")
			}
			sidecar, err := json.Marshal(map[string]string{"simple_context_manager": string(encoded)})
			if err != nil {
				t.Fatal(err)
			}
			store := &checkpointMemory{values: map[string][]byte{"fixture": raw, "deepagent_graph_state_:fixture": sidecar}}
			history := conversation.New("thread", nil, nil, nil)
			if mode == "matching" || mode == "different" {
				actual := append([]*schema.Message(nil), messages...)
				if mode == "different" {
					actual[0] = schema.UserMessage("another question")
				}
				if err := history.AddHistory(ctx, "run", actual...); err != nil {
					t.Fatal(err)
				}
			}
			before, err := json.Marshal(history.History(ctx))
			if err != nil {
				t.Fatal(err)
			}
			counter := &countingTool{}
			model := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("resumed", nil)}}}
			agent, err := New(ctx, WithConfig(&Config{Model: model, ThreadID: "thread", RunID: "run", Conversation: history, CheckpointStore: store, ToolDescriptors: []tools.Descriptor{{Tool: counter}}}))
			if err != nil {
				t.Fatal(err)
			}
			defer agent.Close(ctx)
			_, err = agent.Run(ctx, nil, WithCheckpointID("fixture"))
			if mode != "matching" {
				if err == nil || counter.count.Load() != 0 || model.calls != 0 {
					t.Fatalf("err=%v tools=%d model=%d", err, counter.count.Load(), model.calls)
				}
				if mode == "invalid" && !strings.Contains(err.Error(), "decode legacy context history") {
					t.Fatal(err)
				}
				after, marshalErr := json.Marshal(history.History(ctx))
				if marshalErr != nil || string(after) != string(before) {
					t.Fatalf("rejected restore changed history: %s -> %s (%v)", before, after, marshalErr)
				}
				if mode != "invalid" && !strings.Contains(err.Error(), "matching durable conversation history") {
					t.Fatal(err)
				}
				if mode != "invalid" {
					correct := conversation.New("thread", nil, nil, nil)
					if err := correct.AddHistory(ctx, "run", messages...); err != nil {
						t.Fatal(err)
					}
					retry, err := New(ctx, WithConfig(&Config{Model: model, ThreadID: "thread", RunID: "run", Conversation: correct, CheckpointStore: store, ToolDescriptors: []tools.Descriptor{{Tool: counter}}}))
					if err != nil {
						t.Fatal(err)
					}
					defer retry.Close(ctx)
					if _, err := retry.Run(ctx, nil, WithCheckpointID("fixture")); err != nil {
						t.Fatalf("corrected history cannot resume original checkpoint: %v", err)
					}
					if counter.count.Load() != 1 || model.calls != 1 {
						t.Fatalf("retry tools=%d model=%d", counter.count.Load(), model.calls)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if counter.count.Load() != 1 || model.calls != 1 || len(model.inputs[0]) != 3 || model.inputs[0][0].Content != "legacy question" {
				t.Fatalf("tools=%d model=%d inputs=%v", counter.count.Load(), model.calls, model.inputs)
			}
			if _, exists := agent.state.Extensions["middleware:simple_context_manager"]; exists {
				t.Fatal("restored duplicate middleware history")
			}
			if _, exists := agent.state.Extensions["legacy_engine_history"]; exists {
				t.Fatal("history reconciliation was not consumed")
			}
		})
	}
}
