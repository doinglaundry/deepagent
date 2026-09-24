package graph

import (
	"context"
	"eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"strings"
	"testing"
)

func TestRun_ResumeContinuesGraphAndModelBudgets(t *testing.T) {
	for _, tc := range []struct {
		name          string
		steps, models int
		wantTools     int32
		wantModel     int
		wantError     string
	}{
		{"exhausted graph", 3, 10, 0, 1, "maximum graph steps"},
		{"one graph step left", 4, 10, 1, 1, "maximum graph steps"},
		{"exhausted model", 20, 1, 1, 1, "maximum model calls"},
		{"enough remaining", 6, 2, 1, 2, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tool := &countingTool{}
			model := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "approved", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}, {schema.AssistantMessage("done", nil)}}}
			cfg := Config{ThreadID: "thread", RunID: "run", Model: model, CheckpointStore: &checkpointMemory{}, MaxSteps: tc.steps, MaxModelCalls: tc.models, ToolDescriptors: []tools.Descriptor{{Tool: tool, RequiresApproval: true}}}
			first, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			_, err = first.Run(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
			info, ok := compose.ExtractInterruptInfo(err)
			if !ok {
				t.Fatalf("initial run did not interrupt: %v", err)
			}
			if first.state.GraphSteps != 3 || first.state.ModelCalls != 1 {
				t.Fatalf("initial budgets=%+v", first.state)
			}
			cfg.Conversation = first.conversation
			if err = first.Close(ctx); err != nil {
				t.Fatal(err)
			}
			next, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer next.Close(ctx)
			out, err := next.Run(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "approved", Approved: true}}))
			if tc.wantError == "" {
				if err != nil || out.Content != "done" {
					t.Fatalf("out=%v err=%v", out, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("wrong budget error: %v", err)
			}
			if tool.count.Load() != tc.wantTools || model.calls != tc.wantModel {
				t.Fatalf("budget exceeded before rejection: tool=%d model=%d", tool.count.Load(), model.calls)
			}
			if next.state.GraphSteps > tc.steps {
				t.Fatalf("checkpoint counter exceeded: %d", next.state.GraphSteps)
			}
		})
	}
}
