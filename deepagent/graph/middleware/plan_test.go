package middleware

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/schema"
)

func TestPlanReminderUsesRestoredRunState(t *testing.T) {
	originalRunState := &agentmodel.RunState{Version: 1, Plan: []agentmodel.PlanStep{{Step: "inspect", Status: "pending"}}}
	encodedRunState, err := json.Marshal(originalRunState)
	if err != nil {
		t.Fatal(err)
	}
	var restoredRunState agentmodel.RunState
	restoreErr := json.Unmarshal(encodedRunState, &restoredRunState)
	if restoreErr != nil {
		t.Fatal(restoreErr)
	}
	ctx := agentmodel.WithRunState(context.Background(), &restoredRunState)
	planMiddleware := NewPlan()
	history := []*agentmodel.Message{agentmodel.NewUserMessage("compacted summary")}
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
	visiblePlanMessages := []*agentmodel.Message{agentmodel.NewAssistantMessage("", []schema.ToolCall{{Function: schema.FunctionCall{Name: "update_plan"}}})}
	messagesWithVisiblePlan, _ := planMiddleware.ModifyModelRequest(ctx, nil, visiblePlanMessages, nil)
	if len(messagesWithVisiblePlan) != 1 {
		t.Fatal("visible plan was repeated")
	}
}

func TestPlanReminderOnlyTrustsAssistantToolCalls(t *testing.T) {
	ctx := agentmodel.WithRunState(context.Background(), &agentmodel.RunState{Plan: []agentmodel.PlanStep{{Step: "inspect", Status: "pending"}}})
	for _, role := range []schema.RoleType{schema.User, schema.Tool, schema.System, schema.Assistant} {
		t.Run(string(role), func(t *testing.T) {
			message := &agentmodel.Message{Role: role, Content: "quoted tool data", ToolCalls: []schema.ToolCall{{Function: schema.FunctionCall{Name: "update_plan"}}}}
			requestMessages := []*agentmodel.Message{message}
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
