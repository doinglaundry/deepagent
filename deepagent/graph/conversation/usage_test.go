package conversation

import (
	"context"
	"testing"

	"eino-cli/deepagent/graph/types"
	messagepkg "eino-cli/deepagent/message"

	"github.com/cloudwego/eino/components/model"
)

func TestContext_CumulativeUsageSurvivesCompaction(t *testing.T) {
	ctx := context.Background()
	conversation := New("thread", &testStore{}, &testCompactor{}, nil, 1024, nil)
	err := conversation.AddHistory(ctx, "run", messagepkg.NewUserMessage("old"), messagepkg.NewAssistantMessage("done", nil), messagepkg.NewUserMessage("new"))
	if err != nil {
		t.Fatal(err)
	}
	conversation.RecordModelUsage(ctx, &model.TokenUsage{PromptTokens: 3, CompletionTokens: 2})
	_, compactErr := conversation.Compact(ctx, "run")
	if compactErr != nil {
		t.Fatal(compactErr)
	}
	conversation.RecordModelUsage(ctx, nil)
	conversation.RecordModelUsage(ctx, &model.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5})
	runUsage := conversation.GetRunUsage()
	if runUsage != (types.Usage{PromptTokens: 6, CompletionTokens: 4, TotalTokens: 10}) {
		t.Fatalf("cumulative=%+v", runUsage)
	}
	contextUsage := conversation.GetContextUsage()
	if contextUsage.CurrentTotal != 5 || contextUsage.ContextWindow != 1024 {
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
	if conversation.GetRunUsage().TotalTokens != 0 || conversation.GetContextUsage().CurrentTotal != 5 {
		t.Fatal("new run must reset only cumulative usage")
	}
}

func TestContextSnapshotRestoresProviderBaselineAtDurableCursor(t *testing.T) {
	ctx := context.Background()
	store := &testStore{}
	tokenCounter := func(messages []*messagepkg.Message) int { return len(messages) * 3 }
	liveConversation := New("thread", store, nil, tokenCounter, 0, nil)
	err := liveConversation.AddHistory(ctx, "run", messagepkg.NewUserMessage("input"), messagepkg.NewAssistantMessage("answer", nil))
	if err != nil {
		t.Fatal(err)
	}
	liveConversation.RecordModelUsage(ctx, &model.TokenUsage{PromptTokens: 400, CompletionTokens: 10, TotalTokens: 410})
	err = liveConversation.AddHistory(ctx, "run", messagepkg.NewToolMessage("addition", "call"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := liveConversation.SnapshotContext()
	restoredConversation := New("thread", store, nil, tokenCounter, 0, nil)
	err = restoredConversation.ReloadHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = restoredConversation.RestoreContext(ctx, snapshot)
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
	if restoredConversation.GetContextUsage().CurrentTotal != 416 {
		t.Fatalf("delta lost: %+v", restoredConversation.GetContextUsage())
	}
}

func TestContextSnapshotRejectsAheadAndPreservesNewerHistory(t *testing.T) {
	ctx := context.Background()
	store := &testStore{}
	liveConversation := New("thread", store, nil, func(messages []*messagepkg.Message) int { return len(messages) * 3 }, 0, nil)
	err := liveConversation.AddHistory(ctx, "run", messagepkg.NewUserMessage("input"))
	if err != nil {
		t.Fatal(err)
	}
	liveConversation.RecordModelUsage(ctx, &model.TokenUsage{TotalTokens: 410})
	snapshot := liveConversation.SnapshotContext()
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
	err = restoredConversation.RestoreContext(ctx, snapshot)
	if err != nil || restoredConversation.GetContextUsage() != previousUsage {
		t.Fatalf("stale snapshot replaced newer context: %+v %v", restoredConversation.GetContextUsage(), err)
	}
	snapshot.HistoryCursor = 3
	err = restoredConversation.RestoreContext(ctx, snapshot)
	if err == nil || restoredConversation.GetContextUsage() != previousUsage {
		t.Fatalf("checkpoint ahead accepted or changed usage: %+v %v", restoredConversation.GetContextUsage(), err)
	}
	snapshot.HistoryCursor = 2
	snapshot.Usage.CurrentTotal = -1
	err = restoredConversation.RestoreContext(ctx, snapshot)
	if err == nil || restoredConversation.GetContextUsage() != previousUsage {
		t.Fatal("invalid snapshot changed usage")
	}
}
