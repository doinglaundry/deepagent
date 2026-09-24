package graph

import (
	"context"
	"eino-cli/deepagent/core/internal/conversation"
	"eino-cli/deepagent/core/runtime/checkpointer"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"encoding/json"
	"github.com/cloudwego/eino/schema"
	"os"
	"strings"
	"testing"
)

func TestCheckpoint_LegacyEngineApprovalResumesWithoutReplayingCompletedTool(t *testing.T) {
	for _, approved := range []bool{true, false} {
		t.Run(map[bool]string{true: "approved", false: "denied"}[approved], func(t *testing.T) {
			ctx := context.Background()
			raw, err := os.ReadFile("../runtime/checkpointer/testdata/legacy_engine_approval.json")
			if err != nil {
				t.Fatal(err)
			}
			var old struct {
				Messages []*schema.Message `json:"messages"`
			}
			if err = json.Unmarshal(raw, &old); err != nil {
				t.Fatal(err)
			}
			history := conversation.New("thread", nil, nil, nil)
			if err = history.AddHistory(ctx, "run", old.Messages...); err != nil {
				t.Fatal(err)
			}
			store := &checkpointMemory{values: map[string][]byte{"fixture": raw}}
			counter := &countingTool{}
			m := &sequenceModel{responses: [][]*schema.Message{{usageReply("resumed")}}}
			a, err := New(ctx, WithConfig(&Config{ThreadID: "thread", RunID: "run", Model: m, Conversation: history, MaxModelCalls: 2, CheckpointStore: store, ToolDescriptors: []tools.Descriptor{{Tool: counter, RequiresApproval: approved}}}))
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close(ctx)
			out, err := a.Run(ctx, nil, WithCheckpointID("fixture"), WithResume("gate"), WithResumeData(map[string]any{"gate": &tools.ApprovalResult{CallID: "blocked", Approved: approved}}))
			if err != nil {
				t.Fatal(err)
			}
			want := int32(0)
			if approved {
				want = 1
			}
			if counter.count.Load() != want || m.calls != 1 || out.Content != "resumed" || a.state.ModelCalls != 2 {
				t.Fatalf("tool=%d model=%d out=%v budget=%d", counter.count.Load(), m.calls, out, a.state.ModelCalls)
			}
			if a.state.Usage != (types.Usage{PromptTokens: 83, CompletionTokens: 22, TotalTokens: 105}) {
				t.Fatalf("legacy cumulative usage lost: %+v", a.state.Usage)
			}
			if len(m.inputs[0]) != 4 || m.inputs[0][2].ToolCallID != "completed" || m.inputs[0][2].Content != "already done" || m.inputs[0][3].ToolCallID != "blocked" {
				t.Fatalf("history duplicated or lost: %+v", m.inputs[0])
			}
			var envelope checkpointer.Envelope
			if err = json.Unmarshal(store.values["fixture"], &envelope); err != nil || envelope.Version != 1 || envelope.GraphVersion != "core-graph-v1" {
				t.Fatalf("migration not persisted: %v %+v", err, envelope)
			}
		})
	}
}

func TestCheckpoint_LegacyEngineClarificationResumesAtQuestion(t *testing.T) {
	ctx := context.Background()
	raw, err := os.ReadFile("../runtime/checkpointer/testdata/legacy_engine_approval.json")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(raw), `"id":"blocked","function":{"name":"counter","arguments":"{}"}`, `"id":"blocked","function":{"name":"ask_user","arguments":"{}"}`)
	text = strings.ReplaceAll(text, `"tool_name":"counter"`, `"tool_name":"ask_user"`)
	text = strings.ReplaceAll(text, `"kind":"approval"`, `"kind":"clarification"`)
	var old struct {
		Messages []*schema.Message `json:"messages"`
	}
	if err = json.Unmarshal([]byte(text), &old); err != nil {
		t.Fatal(err)
	}
	history := conversation.New("thread", nil, nil, nil)
	if err = history.AddHistory(ctx, "run", old.Messages...); err != nil {
		t.Fatal(err)
	}
	store := &checkpointMemory{values: map[string][]byte{"fixture": []byte(text)}}
	counter := &countingTool{}
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("answered", nil)}}}
	a, err := New(ctx, WithConfig(&Config{ThreadID: "thread", RunID: "run", Model: m, Conversation: history, CheckpointStore: store, ToolDescriptors: []tools.Descriptor{{Tool: counter}, {Tool: tools.GetFollowUpTool()}}}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	out, err := a.Run(ctx, nil, WithCheckpointID("fixture"), WithResume("gate"), WithResumeData(map[string]any{"gate": &tools.FollowUpInfo{UserAnswer: "blue"}}))
	if err != nil {
		t.Fatal(err)
	}
	if counter.count.Load() != 0 || m.calls != 1 || out.Content != "answered" || len(m.inputs[0]) != 4 || m.inputs[0][3].Content != "blue" {
		t.Fatalf("unexpected replay: tool=%d model=%d out=%v history=%v", counter.count.Load(), m.calls, out, m.inputs)
	}
	if a.state.Usage.TotalTokens != 100 {
		t.Fatalf("response without usage reset restored total: %+v", a.state.Usage)
	}
}

func TestCheckpoint_LegacyEngineHistoryMismatchCannotExecuteTools(t *testing.T) {
	ctx := context.Background()
	raw, err := os.ReadFile("../runtime/checkpointer/testdata/legacy_engine_approval.json")
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err = json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	wire["pending"] = json.RawMessage(`[{"id":"untrusted-pending","kind":"user","text":"must not write"}]`)
	raw, err = json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	store := &checkpointMemory{values: map[string][]byte{"fixture": raw}}
	counter := &countingTool{}
	m := &sequenceModel{}
	a, err := New(ctx, WithConfig(&Config{ThreadID: "thread", RunID: "run", Model: m, CheckpointStore: store, ToolDescriptors: []tools.Descriptor{{Tool: counter, RequiresApproval: true}}}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	_, err = a.Run(ctx, nil, WithCheckpointID("fixture"), WithResume("gate"), WithResumeData(map[string]any{"gate": &tools.ApprovalResult{CallID: "blocked", Approved: true}}))
	if err == nil || !strings.Contains(err.Error(), "matching durable conversation history") || counter.count.Load() != 0 || m.calls != 0 || len(a.conversation.History(ctx)) != 0 {
		t.Fatalf("unreconciled history executed: err=%v tools=%d models=%d", err, counter.count.Load(), m.calls)
	}
}
