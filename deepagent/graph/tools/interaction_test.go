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
	for _, arguments := range []string{
		`{"plan":[{"step":"inspect","status":"in_progress"}]}`,
		`{"todos":[{"content":"inspect","status":"in_progress"}],"plan":"ignored"}`,
		`{"plan":"inspect"}`,
		`"inspect"`,
		`inspect`,
	} {
		t.Run(arguments, func(t *testing.T) {
			runState := &types.RunState{}
			ctx := types.WithRunState(context.Background(), runState)
			output, err := NewUpdatePlanTool(nil).Tool.(einotool.InvokableTool).InvokableRun(ctx, arguments)
			if err != nil {
				t.Fatal(err)
			}
			var planUpdate PlanUpdate
			decodeErr := json.Unmarshal([]byte(output), &planUpdate)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if len(runState.Plan) != 1 || runState.Plan[0].Step != "inspect" || runState.Plan[0].Status != "in_progress" || len(planUpdate.Plan) != 1 {
				t.Fatalf("state=%+v output=%s", runState.Plan, output)
			}
		})
	}
}

func TestUpdatePlanFailedPublishPreservesState(t *testing.T) {
	runState := &types.RunState{Plan: []types.PlanStep{{Step: "old", Status: "pending"}}}
	publishErr := errors.New("publish failed")
	planDescriptor := NewUpdatePlanTool(func(context.Context, PlanUpdate) error { return publishErr })
	_, err := planDescriptor.Tool.(einotool.InvokableTool).InvokableRun(types.WithRunState(context.Background(), runState), `{"plan":"new"}`)
	if !errors.Is(err, publishErr) || runState.Plan[0].Step != "old" {
		t.Fatalf("err=%v plan=%v", err, runState.Plan)
	}
}

func TestUpdatePlanPublishesValidatedPlan(t *testing.T) {
	var publishedUpdate PlanUpdate
	planDescriptor := NewUpdatePlanTool(func(_ context.Context, planUpdate PlanUpdate) error {
		publishedUpdate = planUpdate
		return nil
	})
	arguments := `{"explanation":"starting","plan":[{"step":"inspect","status":"in_progress"}]}`
	_, err := planDescriptor.Tool.(einotool.InvokableTool).InvokableRun(context.Background(), arguments)
	if err != nil {
		t.Fatal(err)
	}
	if publishedUpdate.Explanation != "starting" || len(publishedUpdate.Plan) != 1 || publishedUpdate.Plan[0].Step != "inspect" {
		t.Fatalf("published update = %+v", publishedUpdate)
	}
}

func TestUpdatePlanRejectsInvalidState(t *testing.T) {
	planDescriptor := NewUpdatePlanTool(nil)
	_, err := planDescriptor.Tool.(einotool.InvokableTool).InvokableRun(context.Background(), `{"plan":[{"step":"inspect","status":"later"}]}`)
	if err == nil {
		t.Fatal("invalid plan status was accepted")
	}
}

type skillTestLoader struct{ items []*skills.SkillMetadata }

func (skillLoader skillTestLoader) ListSkills(context.Context) ([]*skills.SkillMetadata, error) {
	return skillLoader.items, nil
}

func TestActivateSkillLoadsInstructionsWithoutMiddleware(t *testing.T) {
	skillPath := filepath.Join(t.TempDir(), "SKILL.md")
	err := os.WriteFile(skillPath, []byte("follow these instructions"), 0600)
	if err != nil {
		t.Fatal(err)
	}
	skillLoader := skillTestLoader{items: []*skills.SkillMetadata{{Name: "review", Path: skillPath}}}
	output, err := NewActivateSkillTool(skillLoader).Tool.(einotool.InvokableTool).InvokableRun(context.Background(), `{"name":"review"}`)
	if err != nil || !strings.Contains(output, "follow these instructions") {
		t.Fatalf("activate_skill = %q, %v", output, err)
	}
}
