package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"eino-cli/deepagent/graph/types"
)

func TestUpdatePlanAliasesShareRunState(t *testing.T) {
	for _, raw := range []string{
		`{"plan":[{"step":"inspect","status":"in_progress"}]}`,
		`{"todos":[{"content":"inspect","status":"in_progress"}],"plan":"ignored"}`,
		`{"plan":"inspect"}`,
		`"inspect"`,
		`inspect`,
	} {
		t.Run(raw, func(t *testing.T) {
			state := &types.RunState{}
			ctx := types.WithRunState(context.Background(), state)
			out, err := NewUpdatePlanTool(nil).InvokableRun(ctx, raw)
			if err != nil {
				t.Fatal(err)
			}
			var update PlanUpdate
			decodeErr := json.Unmarshal([]byte(out), &update)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if len(state.Plan) != 1 || state.Plan[0].Step != "inspect" || state.Plan[0].Status != "in_progress" || len(update.Plan) != 1 {
				t.Fatalf("state=%+v output=%s", state.Plan, out)
			}
		})
	}
}

func TestUpdatePlanFailedPublishPreservesState(t *testing.T) {
	state := &types.RunState{Plan: []types.PlanStep{{Step: "old", Status: "pending"}}}
	want := errors.New("publish failed")
	tool := NewUpdatePlanTool(func(context.Context, PlanUpdate) error { return want })
	_, err := tool.InvokableRun(types.WithRunState(context.Background(), state), `{"plan":"new"}`)
	if !errors.Is(err, want) || state.Plan[0].Step != "old" {
		t.Fatalf("err=%v plan=%v", err, state.Plan)
	}
}
