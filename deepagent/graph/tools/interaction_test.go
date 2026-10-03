package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eino-cli/deepagent/graph/skills"
	"eino-cli/deepagent/graph/types"

	einotool "github.com/cloudwego/eino/components/tool"
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
			out, err := NewUpdatePlanTool(nil).Tool.(einotool.InvokableTool).InvokableRun(ctx, raw)
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
	_, err := tool.Tool.(einotool.InvokableTool).InvokableRun(types.WithRunState(context.Background(), state), `{"plan":"new"}`)
	if !errors.Is(err, want) || state.Plan[0].Step != "old" {
		t.Fatalf("err=%v plan=%v", err, state.Plan)
	}
}

func TestUpdatePlanPublishesValidatedPlan(t *testing.T) {
	var got PlanUpdate
	runner := NewUpdatePlanTool(func(_ context.Context, update PlanUpdate) error {
		got = update
		return nil
	})
	input := `{"explanation":"starting","plan":[{"step":"inspect","status":"in_progress"}]}`
	_, err := runner.Tool.(einotool.InvokableTool).InvokableRun(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Explanation != "starting" || len(got.Plan) != 1 || got.Plan[0].Step != "inspect" {
		t.Fatalf("published update = %+v", got)
	}
}

func TestUpdatePlanRejectsInvalidState(t *testing.T) {
	runner := NewUpdatePlanTool(nil)
	_, err := runner.Tool.(einotool.InvokableTool).InvokableRun(context.Background(), `{"plan":[{"step":"inspect","status":"later"}]}`)
	if err == nil {
		t.Fatal("invalid plan status was accepted")
	}
}

type skillTestLoader struct{ items []*skills.SkillMetadata }

func (l skillTestLoader) ListSkills(context.Context) ([]*skills.SkillMetadata, error) {
	return l.items, nil
}

func TestActivateSkillLoadsInstructionsWithoutMiddleware(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SKILL.md")
	err := os.WriteFile(path, []byte("follow these instructions"), 0600)
	if err != nil {
		t.Fatal(err)
	}
	loader := skillTestLoader{items: []*skills.SkillMetadata{{Name: "review", Path: path}}}
	output, err := NewActivateSkillTool(loader).Tool.(einotool.InvokableTool).InvokableRun(context.Background(), `{"name":"review"}`)
	if err != nil || !strings.Contains(output, "follow these instructions") {
		t.Fatalf("activate_skill = %q, %v", output, err)
	}
}
