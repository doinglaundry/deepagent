package plan

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/components/tool"
)

func TestUpdatePlanPublishesValidatedPlan(t *testing.T) {
	var got PlanUpdate
	middleware := New(&PlanMiddlewareConfig{OnPlanUpdate: func(_ context.Context, update PlanUpdate) error {
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
	if _, err = runner.InvokableRun(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if got.Explanation != "starting" || len(got.Plan) != 1 || got.Plan[0].Step != "inspect" {
		t.Fatalf("published update = %+v", got)
	}
}

func TestUpdatePlanRejectsInvalidState(t *testing.T) {
	middleware := New(nil)
	items, _ := middleware.Tools(context.Background())
	runner := items[0].(tool.InvokableTool)
	if _, err := runner.InvokableRun(context.Background(), `{"plan":[{"step":"inspect","status":"later"}]}`); err == nil {
		t.Fatal("invalid plan status was accepted")
	}
}
