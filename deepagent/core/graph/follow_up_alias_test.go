package graph

import (
	"context"
	"reflect"
	"testing"

	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

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
					observed, _ = e.Data.(*schema.Message)
				}
				return nil
			}
			cfg := Config{Emit: emit, Model: m, RunID: "run", CheckpointStore: &checkpointMemory{}, Tools: nil, ToolDescriptors: []tools.Descriptor{{Tool: tools.GetFollowUpTool()}}}
			a, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close(ctx)
			_, err = a.Run(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
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
			resumed, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer resumed.Close(ctx)
			result, err := resumed.Run(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.FollowUpInfo{UserAnswer: "yes"}}))
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
