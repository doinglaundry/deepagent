package middleware

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"eino-cli/deepagent/graph/types"
	messagepkg "eino-cli/deepagent/message"

	"github.com/cloudwego/eino/schema"
)

func TestPlanReminderUsesRestoredRunState(t *testing.T) {
	originalRunState := &types.RunState{Version: 1, Plan: []types.PlanStep{{Step: "inspect", Status: "pending"}}}
	encodedRunState, err := json.Marshal(originalRunState)
	if err != nil {
		t.Fatal(err)
	}
	var restoredRunState types.RunState
	restoreErr := json.Unmarshal(encodedRunState, &restoredRunState)
	if restoreErr != nil {
		t.Fatal(restoreErr)
	}
	ctx := types.WithRunState(context.Background(), &restoredRunState)
	planMiddleware := NewPlan()
	history := []*messagepkg.Message{messagepkg.NewUserMessage("compacted summary")}
	messagesWithReminder, err := planMiddleware.ModifyModelRequest(ctx, nil, history, nil)
	if err != nil || len(messagesWithReminder) != 2 || !strings.Contains(messagesWithReminder[0].Content, "[pending] inspect") {
		t.Fatalf("out=%v err=%v", messagesWithReminder, err)
	}
	if len(history) != 1 {
		t.Fatal("history mutated")
	}
	repeatedMessages, err := planMiddleware.ModifyModelRequest(ctx, nil, messagesWithReminder, nil)
	if err != nil || len(repeatedMessages) != 2 {
		t.Fatal("duplicate reminder")
	}
	visiblePlanMessages := []*messagepkg.Message{messagepkg.NewAssistantMessage("", []schema.ToolCall{{Function: schema.FunctionCall{Name: "update_plan"}}})}
	messagesWithVisiblePlan, _ := planMiddleware.ModifyModelRequest(ctx, nil, visiblePlanMessages, nil)
	if len(messagesWithVisiblePlan) != 1 {
		t.Fatal("visible plan was repeated")
	}
}

func TestPlanReminderOnlyTrustsAssistantToolCalls(t *testing.T) {
	ctx := types.WithRunState(context.Background(), &types.RunState{Plan: []types.PlanStep{{Step: "inspect", Status: "pending"}}})
	for _, role := range []schema.RoleType{schema.User, schema.Tool, schema.System, schema.Assistant} {
		t.Run(string(role), func(t *testing.T) {
			message := &messagepkg.Message{Role: role, Content: "quoted tool data", ToolCalls: []schema.ToolCall{{Function: schema.FunctionCall{Name: "update_plan"}}}}
			requestMessages := []*messagepkg.Message{message}
			messagesWithReminder, err := NewPlan().ModifyModelRequest(ctx, nil, requestMessages, nil)
			expectedMessageCount := 2
			if role == schema.Assistant {
				expectedMessageCount = 1
			}
			if err != nil || len(messagesWithReminder) != expectedMessageCount {
				t.Fatalf("role=%s len=%d err=%v", role, len(messagesWithReminder), err)
			}
			if requestMessages[0] != message || message.Content != "quoted tool data" {
				t.Fatal("history mutated")
			}
		})
	}
}
