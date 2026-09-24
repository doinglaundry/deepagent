//go:build !windows

package thread

import (
	"testing"

	"eino-cli/deepagent/core/runtime/agentthread"
	"eino-cli/deepagent/core/types"
	eventpkg "eino-cli/deepagent/protocol/event"
	inputpkg "eino-cli/deepagent/protocol/input"
)

func TestThreadPlanInputPreservesQuestionsAndResumeIdentity(t *testing.T) {
	info := &types.RequestUserInputInfo{Questions: []types.RequestUserInputQuestion{{ID: "q1", Header: "scope", Question: "Which directory?", Options: []types.RequestUserInputOption{{Label: "src", Description: "Source files"}}}, {ID: "q2", Question: "Additional constraints?"}}}
	kind, raw, err := agentEventPayloadForOutput(agentthread.Event{Type: agentthread.EventInterrupted, Payload: agentthread.InterruptedPayload{InterruptID: "interrupt", CheckpointID: "checkpoint", Info: info}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := raw.(*eventpkg.PlanInputRequiredEventPayload)
	if kind != eventpkg.EventTypeInputRequired || !ok {
		t.Fatalf("kind=%v payload=%T", kind, raw)
	}
	if payload.InterruptID != "interrupt" || payload.CheckpointID != "checkpoint" || len(payload.Questions) != 2 {
		t.Fatalf("payload=%+v", payload)
	}
	q := payload.Questions[0]
	if q.ID != "q1" || q.Header != "scope" || q.Question != "Which directory?" || len(q.Options) != 1 || q.Options[0].Label != "src" || q.Options[0].Description != "Source files" {
		t.Fatalf("question=%+v", q)
	}
	info.Questions[0].Options[0].Label = "changed"
	if q.Options[0].Label != "src" {
		t.Fatal("transport shares input options")
	}
	if payload.Questions[1].Question != "Additional constraints?" {
		t.Fatal("second question lost")
	}
}
func TestThreadPlanInputAnswerOwnsItsData(t *testing.T) {
	if planInputResponse(nil) != nil {
		t.Fatal("nil response changed")
	}
	source := &inputpkg.RequestUserInputResponse{Answers: map[string]inputpkg.RequestUserInputAnswer{"q1": {Answers: []string{"src", "tests"}}, "q2": {Answers: []string{"preserve API"}}}}
	answer := planInputResponse(source)
	source.Answers["q1"].Answers[0] = "changed"
	delete(source.Answers, "q2")
	if len(answer.Answers) != 2 || answer.Answers["q1"].Answers[0] != "src" || answer.Answers["q1"].Answers[1] != "tests" || answer.Answers["q2"].Answers[0] != "preserve API" {
		t.Fatalf("answer=%+v", answer)
	}
}
