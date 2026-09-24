package agentthread

import (
	"context"
	"testing"
	"time"

	"eino-cli/deepagent/core/graph"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/schema"
)

func TestPlanEventAdapterPreservesPayload(t *testing.T) {
	update := tools.PlanUpdate{Explanation: "starting", Plan: []tools.PlanStep{{Step: "inspect", Status: "in_progress"}}}
	payload := adaptPayload(types.RuntimeEvent{Kind: string(EventPlanUpdated), Data: update}).(PlanUpdatedPayload)
	if payload.Explanation != "starting" || len(payload.Plan) != 1 || payload.Plan[0].Step != "inspect" || payload.Plan[0].Status != PlanStepStatusInProgress {
		t.Fatalf("payload=%+v", payload)
	}
}

func TestToolEventAdapterPreservesMetadata(t *testing.T) {
	call := types.ToolCall{ID: "call", Name: "read_file", Arguments: `{"path":"a"}`}
	started := time.Now()
	state := types.ToolCallState{Call: call, StartedAt: started, Result: &types.ToolResult{CallID: call.ID, Content: "output"}}
	start := adaptPayload(types.RuntimeEvent{Kind: string(EventToolStart), CallID: call.ID, Data: state}).(ToolStartPayload)
	chunk := adaptPayload(types.RuntimeEvent{Kind: string(EventToolCallOutputChunk), CallID: call.ID, Data: types.ToolOutputChunk{Call: call, Content: "out"}}).(ToolCallOutputChunkPayload)
	end := adaptPayload(types.RuntimeEvent{Kind: string(EventToolEnd), CallID: call.ID, Data: state}).(ToolEndPayload)
	if start.Name != call.Name || start.CallID != call.ID || start.Args != call.Arguments {
		t.Fatalf("start=%+v", start)
	}
	if chunk.Name != call.Name || chunk.CallID != call.ID || chunk.Chunk != "out" {
		t.Fatalf("chunk=%+v", chunk)
	}
	if end.Name != call.Name || end.CallID != call.ID || end.ArgumentsInJSON != call.Arguments || !end.ToolStartTime.Equal(started) || end.Result != "output" {
		t.Fatalf("end=%+v", end)
	}
}

func TestModelEventsShareResponseIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	events := make(chan Event, 32)
	thread := New("thread", &RunConfig{Agent: graph.Config{Model: &threadModel{}}}, events, ThreadOptions{})
	accepted, err := thread.SubmitInput(ctx, schema.UserMessage("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if err := accepted.RunHandle.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	var tokenID, finalID string
	for len(events) > 0 {
		e := <-events
		switch p := e.Payload.(type) {
		case LLMTokenChunk:
			tokenID = p.LLMResponseID
		case LLMEnd:
			finalID = p.LLMResponseID
		}
	}
	if tokenID == "" || tokenID != finalID {
		t.Fatalf("token=%q final=%q", tokenID, finalID)
	}
}
