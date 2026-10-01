package deepagents

import (
	"context"
	"eino-cli/deepagent/core/runtime/checkpointer"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"reflect"
	"strings"
	"testing"
)

func TestCheckpoint_PendingApprovalSurvivesPolicyChange(t *testing.T) {
	ctx := context.Background()
	tool := &countingTool{}
	m := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})},
		{schema.AssistantMessage("done", nil)},
	}}
	cfg := Config{Model: m, RunID: "run", CheckpointStore: &checkpointMemory{}, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool, RequiresApproval: true}}}
	first, err := NewRun(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close(ctx)
	_, err = first.Execute(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok || len(info.InterruptContexts) != 1 {
		t.Fatalf("interrupt=%+v err=%v", info, err)
	}
	if len(first.state.Pending) != 1 || first.state.Pending[0].InterruptID != info.InterruptContexts[0].ID || first.state.Pending[0].CallID != "call" || first.state.Pending[0].CheckpointID != "checkpoint" {
		t.Errorf("pending=%+v", first.state.Pending)
	}
	cfg.Conversation = first.conversation
	cfg.ToolDescriptors[0].RequiresApproval = false
	second, err := NewRun(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(ctx)
	_, err = second.Execute(ctx, nil, WithCheckpointID("checkpoint"))
	again, ok := compose.ExtractInterruptInfo(err)
	if !ok || tool.count.Load() != 0 {
		t.Fatalf("approval bypassed: calls=%d err=%v", tool.count.Load(), err)
	}
	third, err := NewRun(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close(ctx)
	_, err = third.Execute(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{again.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "call", Approved: true}}))
	if err != nil {
		t.Fatal(err)
	}
	if tool.count.Load() != 1 || len(third.state.Pending) != 0 {
		t.Fatalf("calls=%d pending=%+v", tool.count.Load(), third.state.Pending)
	}
}

type pendingWriteFailure struct {
	checkpointMemory
	writes  int
	failure error
	initial []byte
}

func (s *pendingWriteFailure) Set(ctx context.Context, id string, raw []byte) error {
	var envelope checkpointer.Envelope
	{
		err := json.Unmarshal(raw, &envelope)
		if err != nil {
			return err
		}
	}
	var snapshot struct {
		MapValues map[string]struct{ JSONValue types.RunState }
	}
	{
		err := json.Unmarshal(envelope.EinoSnapshot, &snapshot)
		if err != nil {
			return err
		}
	}
	if len(snapshot.MapValues["State"].JSONValue.Pending) == 0 {
		return s.checkpointMemory.Set(ctx, id, raw)
	}
	s.writes++
	if s.writes == 1 {
		s.initial = append([]byte(nil), raw...)
	}
	if s.writes == 2 {
		return s.failure
	}
	return s.checkpointMemory.Set(ctx, id, raw)
}

func TestCheckpoint_PendingWriteFailureCannotLoseApprovalObligation(t *testing.T) {
	ctx := context.Background()
	tool := &countingTool{}
	store := &pendingWriteFailure{failure: errors.New("pending metadata write failed")}
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}}}
	cfg := Config{Model: m, RunID: "run", CheckpointStore: store, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool, RequiresApproval: true}}}
	a, err := NewRun(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	_, err = a.Execute(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
	if !errors.Is(err, store.failure) {
		t.Fatalf("failure lost: %v", err)
	}
	{
		_, blocked := compose.ExtractInterruptInfo(err)
		if blocked {
			t.Fatal("failed metadata write published a blocked result")
		}
	}
	cfg.Conversation = a.conversation
	cfg.ToolDescriptors[0].RequiresApproval = false
	resumed, err := NewRun(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close(ctx)
	_, err = resumed.Execute(ctx, nil, WithCheckpointID("checkpoint"))
	if err == nil || !strings.Contains(err.Error(), "terminal") || tool.count.Load() != 0 {
		t.Fatalf("failed run remained resumable: calls=%d err=%v", tool.count.Load(), err)
	}
	// If the process dies before terminal cleanup, the original Eino write
	// must still retain the approval obligation without metadata enrichment.
	cfg.CheckpointStore = &checkpointMemory{values: map[string][]byte{"checkpoint": store.initial}}
	crashResume, err := NewRun(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer crashResume.Close(ctx)
	_, err = crashResume.Execute(ctx, nil, WithCheckpointID("checkpoint"))
	{
		_, blocked := compose.ExtractInterruptInfo(err)
		if !blocked || tool.count.Load() != 0 {
			t.Fatalf("initial snapshot lost approval obligation: calls=%d err=%v", tool.count.Load(), err)
		}
	}
}

// Capture both Eino's original write and the later interrupt-ID enrichment.
// The original write must already be safe if enrichment fails or the process exits.
type pendingSnapshotStore struct {
	checkpointMemory
	writes [][]byte
}

func (s *pendingSnapshotStore) Set(ctx context.Context, id string, raw []byte) error {
	s.writes = append(s.writes, append([]byte(nil), raw...))
	return s.checkpointMemory.Set(ctx, id, raw)
}

func TestCheckpoint_CompletedApprovalRemovedBeforeNextInterruptSnapshot(t *testing.T) {
	for _, approved := range []bool{true, false} {
		for _, answerCallID := range []string{"first", ""} {
			t.Run(fmt.Sprintf("approved=%t/answerCallID=%s", approved, answerCallID), func(t *testing.T) {
				ctx := context.Background()
				counter := &countingTool{}
				store := &pendingSnapshotStore{}
				m := &sequenceModel{responses: [][]*schema.Message{
					{schema.AssistantMessage("", []schema.ToolCall{
						{ID: "first", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}},
						{ID: "second", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}},
					})},
					{schema.AssistantMessage("done", nil)},
				}}
				cfg := Config{Model: m, RunID: "run", CheckpointStore: store, ToolDescriptors: []tools.ToolDescriptor{{Tool: counter, RequiresApproval: true}}}
				a, err := NewRun(ctx, WithConfig(&cfg))
				if err != nil {
					t.Fatal(err)
				}
				defer a.Close(ctx)
				_, err = a.Execute(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
				info, ok := compose.ExtractInterruptInfo(err)
				if !ok || len(info.InterruptContexts) != 1 {
					t.Fatalf("first interrupt: %v", err)
				}
				cfg.Conversation = a.conversation
				resumed, err := NewRun(ctx, WithConfig(&cfg))
				if err != nil {
					t.Fatal(err)
				}
				defer resumed.Close(ctx)
				before := len(store.writes)
				_, err = resumed.Execute(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{
					info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: answerCallID, Approved: approved},
				}))
				expectedCalls := int32(0)
				if approved {
					expectedCalls = 1
				}
				second, ok := compose.ExtractInterruptInfo(err)
				if !ok || len(second.InterruptContexts) != 1 || counter.count.Load() != expectedCalls {
					t.Fatalf("second interrupt: %v calls=%d", err, counter.count.Load())
				}
				if len(store.writes) <= before {
					t.Fatal("no checkpoint saved")
				}
				var envelope checkpointer.Envelope
				if approved {
					// The execution fence precedes Eino's next interrupt snapshot.
					before++
				}
				if len(store.writes) <= before {
					t.Fatal("no interrupt snapshot after execution fence")
				}
				{
					err := json.Unmarshal(store.writes[before], &envelope)
					if err != nil {
						t.Fatal(err)
					}
				}
				var snapshot struct {
					MapValues map[string]struct{ JSONValue types.RunState }
				}
				{
					err := json.Unmarshal(envelope.EinoSnapshot, &snapshot)
					if err != nil {
						t.Fatal(err)
					}
				}
				state := snapshot.MapValues["State"].JSONValue
				if len(state.Pending) != 1 || state.Pending[0].CallID != "second" {
					t.Fatalf("original Eino snapshot retained resolved approval: %+v", state.Pending)
				}
				if state.Calls[0].Status != types.CallCompleted || state.Calls[1].Status != types.CallBlocked {
					t.Fatalf("calls=%+v", state.Calls)
				}
				cfg.Conversation = resumed.conversation
				last, err := NewRun(ctx, WithConfig(&cfg))
				if err != nil {
					t.Fatal(err)
				}
				defer last.Close(ctx)
				_, err = last.Execute(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{
					second.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "second", Approved: true},
				}))
				if err != nil || counter.count.Load() != expectedCalls+1 || len(last.state.Pending) != 0 {
					t.Fatalf("final err=%v calls=%d pending=%+v", err, counter.count.Load(), last.state.Pending)
				}

			})
		}
	}
}

func TestRun_FollowUpArgumentAliasesPreserveQuestionAndResume(t *testing.T) {
	for _, raw := range []string{
		`{"question":" Choose ","context":" Detail ","options":[" yes ","","no"]}`,
		`{"prompt":" Choose ","context":" Detail ","options":"[\" yes \",\"\",\"no\"]"}`,
		`{"message":" Choose ","context":" Detail ","options":["yes","no"]}`,
	} {
		t.Run(raw, func(t *testing.T) {
			ctx := context.Background()
			m := &sequenceModel{responses: [][]*schema.Message{
				{schema.AssistantMessage("", []schema.ToolCall{{ID: "question", Function: schema.FunctionCall{Name: "ask_user", Arguments: raw}}})},
				{schema.AssistantMessage("done", nil)},
			}}
			var observed *schema.Message
			emit := func(_ context.Context, e types.RuntimeEvent) error {
				if e.Kind == "llm_end" {
					payload := e.Data.(types.LLMEnd)
					observed = payload.Message
				}
				return nil
			}
			cfg := Config{Emit: emit, Model: m, RunID: "run", CheckpointStore: &checkpointMemory{}, ToolDescriptors: []tools.ToolDescriptor{{Tool: tools.GetFollowUpTool()}}}
			a, err := NewRun(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close(ctx)
			_, err = a.Execute(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
			info, ok := compose.ExtractInterruptInfo(err)
			if !ok || len(info.InterruptContexts) != 1 {
				t.Fatalf("interrupt=%+v err=%v", info, err)
			}
			if observed == nil || len(observed.ToolCalls) != 1 || observed.ToolCalls[0].Function.Arguments != raw {
				t.Fatalf("event lost original clarification call: %+v", observed)
			}
			question, ok := info.InterruptContexts[0].Info.(*tools.FollowUpInfo)
			if !ok || question.Question != "Detail\n\nChoose" || !reflect.DeepEqual(question.Questions, []string{"yes", "no"}) {
				t.Fatalf("question=%+v", question)
			}
			cfg.Conversation = a.conversation
			resumed, err := NewRun(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer resumed.Close(ctx)
			result, err := resumed.Execute(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.FollowUpInfo{UserAnswer: "yes"}}))
			if err != nil || result == nil || result.Content != "done" {
				t.Fatalf("result=%v err=%v", result, err)
			}
			found := false
			for _, message := range resumed.conversation.History(ctx) {
				if message.Role == schema.Tool && message.ToolCallID == "question" && message.Content == "yes" {
					found = true
				}
			}
			if !found {
				t.Fatal("resume answer missing from conversation")
			}
		})
	}
}
