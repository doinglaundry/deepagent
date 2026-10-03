package middleware

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"eino-cli/deepagent/graph/types"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestUpdatePlanPublishesValidatedPlan(t *testing.T) {
	var got PlanUpdate
	middleware := NewPlan(&PlanMiddlewareConfig{OnPlanUpdate: func(_ context.Context, update PlanUpdate) error {
		got = update
		return nil
	}})
	tools, err := middleware.Tools(context.Background())
	if err != nil || len(tools) != 1 {
		t.Fatalf("Tools() = %d, %v", len(tools), err)
	}
	runner, ok := tools[0].(tool.InvokableTool)
	if !ok {
		t.Fatal("update_plan is not invokable")
	}
	input := `{"explanation":"starting","plan":[{"step":"inspect","status":"in_progress"}]}`
	_, err = runner.InvokableRun(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Explanation != "starting" || len(got.Plan) != 1 || got.Plan[0].Step != "inspect" {
		t.Fatalf("published update = %+v", got)
	}
}

func TestPlanReminderUsesRestoredRunState(t *testing.T) {
	before := &types.RunState{Version: 1, Plan: []types.PlanStep{{Step: "inspect", Status: "pending"}}}
	raw, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	var restored types.RunState
	decodeErr := json.Unmarshal(raw, &restored)
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	ctx := types.WithRunState(context.Background(), &restored)
	m := NewPlan(nil)
	history := []*schema.Message{schema.UserMessage("compacted summary")}
	out, err := m.ModifyModelRequest(ctx, nil, history, nil)
	if err != nil || len(out) != 2 || !strings.Contains(out[0].Content, "[pending] inspect") {
		t.Fatalf("out=%v err=%v", out, err)
	}
	if len(history) != 1 {
		t.Fatal("history mutated")
	}
	again, err := m.ModifyModelRequest(ctx, nil, out, nil)
	if err != nil || len(again) != 2 {
		t.Fatal("duplicate reminder")
	}
	visible := []*schema.Message{schema.AssistantMessage("", []schema.ToolCall{{Function: schema.FunctionCall{Name: "update_plan"}}})}
	modifyModelRequestOut, _ := m.ModifyModelRequest(ctx, nil, visible, nil)
	if len(modifyModelRequestOut) != 1 {
		t.Fatal("visible plan was repeated")
	}
}

func TestUpdatePlanRejectsInvalidState(t *testing.T) {
	middleware := NewPlan(nil)
	items, _ := middleware.Tools(context.Background())
	runner := items[0].(tool.InvokableTool)
	_, err := runner.InvokableRun(context.Background(), `{"plan":[{"step":"inspect","status":"later"}]}`)
	if err == nil {
		t.Fatal("invalid plan status was accepted")
	}
}

func TestPlanReminderOnlyTrustsAssistantToolCalls(t *testing.T) {
	ctx := types.WithRunState(context.Background(), &types.RunState{Plan: []types.PlanStep{{Step: "inspect", Status: "pending"}}})
	for _, role := range []schema.RoleType{schema.User, schema.Tool, schema.System, schema.Assistant} {
		t.Run(string(role), func(t *testing.T) {
			message := &schema.Message{Role: role, Content: "quoted tool data", ToolCalls: []schema.ToolCall{{Function: schema.FunctionCall{Name: "update_plan"}}}}
			input := []*schema.Message{message}
			out, err := NewPlan(nil).ModifyModelRequest(ctx, nil, input, nil)
			want := 2
			if role == schema.Assistant {
				want = 1
			}
			if err != nil || len(out) != want {
				t.Fatalf("role=%s len=%d err=%v", role, len(out), err)
			}
			if input[0] != message || message.Content != "quoted tool data" {
				t.Fatal("history mutated")
			}
		})
	}
}
