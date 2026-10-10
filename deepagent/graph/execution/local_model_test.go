package execution

import (
	"context"
	"eino-cli/deepagent/graph/tools"
	agentmodel "eino-cli/deepagent/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"strings"
	"testing"
)

func TestLocalModelFingerprintAndCompletedToolSurviveCheckpoint(t *testing.T) {
	ctx := context.Background()
	countingLocalModelTool := &namedCountingTool{name: "ask_local_model"}
	apiModel := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "local", Function: schema.FunctionCall{Name: "ask_local_model", Arguments: `{"prompt":"my preference"}`}}})},
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "ask", Function: schema.FunctionCall{Name: "ask_user", Arguments: `{"question":"Continue?"}`}}})},
		{schema.AssistantMessage("done", nil)},
	}}
	config := Config{Model: apiModel, RunID: "personal", LocalModelParametersFingerprint: "v1", CheckpointStore: &checkpointMemory{}, ToolDescriptors: []agentmodel.ToolDescriptor{{Tool: countingLocalModelTool, ReadOnly: true}, tools.NewFollowUpTool()}}
	graph, err := New(ctx, WithConfig(&config))
	if err != nil {
		t.Fatal(err)
	}
	_, err = graph.Invoke(ctx, []*agentmodel.Message{agentmodel.NewUserMessage("go")}, WithCheckpointID("local-checkpoint"))
	interrupt, ok := compose.ExtractInterruptInfo(err)
	if !ok || countingLocalModelTool.count != 1 {
		t.Fatalf("calls=%d interrupt=%v err=%v", countingLocalModelTool.count, ok, err)
	}
	hasLocalModelAnswer := false
	for _, message := range apiModel.inputs[1] {
		if message.Role == schema.Tool && message.Content == countingLocalModelTool.name {
			hasLocalModelAnswer = true
		}
	}
	if !hasLocalModelAnswer {
		t.Fatal("local answer did not return to cloud model")
	}
	config.Conversation = graph.conversation
	config.LocalModelParametersFingerprint = "v2"
	changedParametersGraph, err := New(ctx, WithConfig(&config))
	if err != nil {
		t.Fatal(err)
	}
	resumeAnswer := WithResumeData(map[string]any{interrupt.InterruptContexts[0].ID: &agentmodel.FollowUpInfo{UserAnswer: "yes"}})
	_, err = changedParametersGraph.Invoke(ctx, nil, WithCheckpointID("local-checkpoint"), resumeAnswer)
	if err == nil || !strings.Contains(err.Error(), "local model version changed") {
		t.Fatalf("changed version accepted: %v", err)
	}
	config.LocalModelParametersFingerprint = "v1"
	restoredGraph, err := New(ctx, WithConfig(&config))
	if err != nil {
		t.Fatal(err)
	}
	result, err := restoredGraph.Invoke(ctx, nil, WithCheckpointID("local-checkpoint"), resumeAnswer)
	if err != nil || result.Content != "done" || countingLocalModelTool.count != 1 {
		t.Fatalf("resume result=%+v calls=%d err=%v", result, countingLocalModelTool.count, err)
	}
}
