package conversation

import (
	"context"
	"encoding/json"
	"testing"

	"eino-cli/deepagent/graph/types"
	messagepkg "eino-cli/deepagent/message"

	"github.com/cloudwego/eino/components/model"
)

func TestContext_CumulativeUsageSurvivesCompaction(t *testing.T) {
	ctx := context.Background()
	conversation := New("thread", &testStore{}, &SummaryCompaction{Model: summaryModel{}, KeepRecent: 1}, nil, 1024, nil)
	err := conversation.AddHistory(ctx, "run", messagepkg.NewUserMessage("old"), messagepkg.NewAssistantMessage("done", nil), messagepkg.NewUserMessage("new"))
	if err != nil {
		t.Fatal(err)
	}
	conversation.RecordModelUsage(ctx, &model.TokenUsage{PromptTokens: 3, CompletionTokens: 2})
	usage, compactErr := conversation.Compact(ctx, "run")
	if compactErr != nil || usage == nil {
		t.Fatalf("compaction did not complete: usage=%+v err=%v", usage, compactErr)
	}
	conversation.RecordModelUsage(ctx, nil)
	conversation.RecordModelUsage(ctx, &model.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5})
	runUsage := conversation.GetRunUsage()
	if runUsage != (types.Usage{PromptTokens: 6, CompletionTokens: 4, TotalTokens: 10}) {
		t.Fatalf("cumulative=%+v", runUsage)
	}
	contextUsage := conversation.GetContextUsage()
	if contextUsage.TotalTokens != 5 || contextUsage.MaxContextTokens != 1024 {
		t.Fatalf("context estimate=%+v", contextUsage)
	}
	cRestoreRunUsageErr := conversation.RestoreRunUsage(ctx, types.Usage{TotalTokens: -1})
	if cRestoreRunUsageErr == nil {
		t.Fatal("negative checkpoint usage accepted")
	}
	if conversation.GetRunUsage().TotalTokens != 10 {
		t.Fatal("invalid snapshot modified tracker")
	}
	restoreRunUsageErr := conversation.RestoreRunUsage(ctx, types.Usage{})
	if restoreRunUsageErr != nil {
		t.Fatal(restoreRunUsageErr)
	}
	if conversation.GetRunUsage().TotalTokens != 0 || conversation.GetContextUsage().TotalTokens != 5 {
		t.Fatal("new run must reset only cumulative usage")
	}
}

func TestContextUsageRestoresAtDurableSequence(t *testing.T) {
	ctx := context.Background()
	store := &testStore{}
	countTokenFunc := func(messages []*messagepkg.Message) int { return len(messages) * 3 }
	liveConversation := New("thread", store, nil, countTokenFunc, 0, nil)
	err := liveConversation.AddHistory(ctx, "run", messagepkg.NewUserMessage("input"), messagepkg.NewAssistantMessage("answer", nil))
	if err != nil {
		t.Fatal(err)
	}
	liveConversation.RecordModelUsage(ctx, &model.TokenUsage{PromptTokens: 400, CompletionTokens: 10, TotalTokens: 410})
	err = liveConversation.AddHistory(ctx, "run", messagepkg.NewToolMessage("addition", "call"))
	if err != nil {
		t.Fatal(err)
	}
	historySeq, contextTokenUsage := liveConversation.SnapshotContext()
	checkpointJSON, err := json.Marshal(types.RunState{HistorySeq: historySeq, ContextUsage: &contextTokenUsage})
	if err != nil {
		t.Fatal(err)
	}
	var runState types.RunState
	err = json.Unmarshal(checkpointJSON, &runState)
	if err != nil {
		t.Fatal(err)
	}
	if runState.ContextUsage == nil || runState.HistorySeq != historySeq {
		t.Fatalf("checkpoint lost context usage or history sequence: %+v", runState)
	}
	restoredConversation := New("thread", store, nil, countTokenFunc, 0, nil)
	err = restoredConversation.ReloadHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = restoredConversation.RestoreContext(ctx, runState.HistorySeq, *runState.ContextUsage)
	if err != nil {
		t.Fatal(err)
	}
	if restoredConversation.GetContextUsage() != liveConversation.GetContextUsage() {
		t.Fatalf("provider baseline lost: restored=%+v original=%+v", restoredConversation.GetContextUsage(), liveConversation.GetContextUsage())
	}
	err = restoredConversation.AddHistory(ctx, "run", messagepkg.NewUserMessage("followup"))
	if err != nil {
		t.Fatal(err)
	}
	contextTokenUsage = restoredConversation.GetContextUsage()
	wantUsage := types.ContextTokenUsage{
		TotalTokens:      416,
		PromptTokens:     400,
		CompletionTokens: 10,
	}
	if contextTokenUsage != wantUsage {
		t.Fatalf("restored usage=%+v want=%+v", contextTokenUsage, wantUsage)
	}
	// 新模型统计替换已有估算；优先采用模型明确返回的总量。
	restoredConversation.RecordModelUsage(ctx, &model.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 99})
	wantUsage.TotalTokens = 99
	wantUsage.PromptTokens = 3
	wantUsage.CompletionTokens = 2
	if restoredConversation.GetContextUsage() != wantUsage {
		t.Fatalf("new model usage did not replace estimate: %+v", restoredConversation.GetContextUsage())
	}
	// 没有总量时使用输入与输出的和。
	restoredConversation.RecordModelUsage(ctx, &model.TokenUsage{PromptTokens: 3, CompletionTokens: 2})
	if restoredConversation.GetContextUsage().TotalTokens != 5 {
		t.Fatalf("missing total was not calculated: %+v", restoredConversation.GetContextUsage())
	}
}

func TestContextUsageRejectsAheadAndPreservesNewerHistory(t *testing.T) {
	ctx := context.Background()
	store := &testStore{}
	liveConversation := New("thread", store, nil, func(messages []*messagepkg.Message) int { return len(messages) * 3 }, 0, nil)
	err := liveConversation.AddHistory(ctx, "run", messagepkg.NewUserMessage("input"))
	if err != nil {
		t.Fatal(err)
	}
	liveConversation.RecordModelUsage(ctx, &model.TokenUsage{TotalTokens: 410})
	historySeq, contextTokenUsage := liveConversation.SnapshotContext()
	err = liveConversation.AddHistory(ctx, "run", messagepkg.NewUserMessage("newer"))
	if err != nil {
		t.Fatal(err)
	}
	restoredConversation := New("thread", store, nil, func(messages []*messagepkg.Message) int { return len(messages) * 3 }, 0, nil)
	err = restoredConversation.ReloadHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	previousUsage := restoredConversation.GetContextUsage()
	err = restoredConversation.RestoreContext(ctx, historySeq, contextTokenUsage)
	if err != nil || restoredConversation.GetContextUsage() != previousUsage {
		t.Fatalf("stale snapshot replaced newer context: %+v %v", restoredConversation.GetContextUsage(), err)
	}
	historySeq = 3
	err = restoredConversation.RestoreContext(ctx, historySeq, contextTokenUsage)
	if err == nil || restoredConversation.GetContextUsage() != previousUsage {
		t.Fatalf("checkpoint ahead accepted or changed usage: %+v %v", restoredConversation.GetContextUsage(), err)
	}
	historySeq = 2
	contextTokenUsage.TotalTokens = -1
	err = restoredConversation.RestoreContext(ctx, historySeq, contextTokenUsage)
	if err == nil || restoredConversation.GetContextUsage() != previousUsage {
		t.Fatal("invalid snapshot changed usage")
	}
}
