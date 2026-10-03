package conversation

import (
	"context"
	"testing"

	"eino-cli/deepagent/graph/types"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestContext_CumulativeUsageSurvivesCompaction(t *testing.T) {
	ctx := context.Background()
	c := New("thread", &testStore{}, &testCompactor{}, nil)
	err := c.AddHistory(ctx, "run", schema.UserMessage("old"), schema.AssistantMessage("done", nil), schema.UserMessage("new"))
	if err != nil {
		t.Fatal(err)
	}
	c.RecordModelUsage(ctx, &model.TokenUsage{PromptTokens: 3, CompletionTokens: 2})
	_, compactErr := c.Compact(ctx, "run")
	if compactErr != nil {
		t.Fatal(compactErr)
	}
	c.RecordModelUsage(ctx, nil)
	c.RecordModelUsage(ctx, &model.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5})
	got := c.RunUsage()
	if got != (types.Usage{PromptTokens: 6, CompletionTokens: 4, TotalTokens: 10}) {
		t.Fatalf("cumulative=%+v", got)
	}
	contextUsageGot := c.ContextUsage()
	if contextUsageGot.CurrentTotal != 5 {
		t.Fatalf("context estimate=%+v", contextUsageGot)
	}
	cRestoreRunUsageErr := c.RestoreRunUsage(ctx, types.Usage{TotalTokens: -1})
	if cRestoreRunUsageErr == nil {
		t.Fatal("negative checkpoint usage accepted")
	}
	if c.RunUsage().TotalTokens != 10 {
		t.Fatal("invalid snapshot modified tracker")
	}
	restoreRunUsageErr := c.RestoreRunUsage(ctx, types.Usage{})
	if restoreRunUsageErr != nil {
		t.Fatal(restoreRunUsageErr)
	}
	if c.RunUsage().TotalTokens != 0 || c.ContextUsage().CurrentTotal != 5 {
		t.Fatal("new run must reset only cumulative usage")
	}
}

func TestContextSnapshotRestoresProviderBaselineAtDurableCursor(t *testing.T) {
	ctx := context.Background()
	store := &testStore{}
	counter := func(messages []*schema.Message) int { return len(messages) * 3 }
	live := New("thread", store, nil, counter)
	err := live.AddHistory(ctx, "run", schema.UserMessage("input"), schema.AssistantMessage("answer", nil))
	if err != nil {
		t.Fatal(err)
	}
	live.RecordModelUsage(ctx, &model.TokenUsage{PromptTokens: 400, CompletionTokens: 10, TotalTokens: 410})
	err = live.AddHistory(ctx, "run", schema.ToolMessage("addition", "call"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := live.SnapshotContext()
	restored := New("thread", store, nil, counter)
	err = restored.ReloadHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = restored.RestoreContext(ctx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if restored.ContextUsage() != live.ContextUsage() {
		t.Fatalf("provider baseline lost: restored=%+v original=%+v", restored.ContextUsage(), live.ContextUsage())
	}
	err = restored.AddHistory(ctx, "run", schema.UserMessage("followup"))
	if err != nil {
		t.Fatal(err)
	}
	if restored.ContextUsage().CurrentTotal != 416 {
		t.Fatalf("delta lost: %+v", restored.ContextUsage())
	}
}

func TestContextSnapshotRejectsAheadAndPreservesNewerHistory(t *testing.T) {
	ctx := context.Background()
	store := &testStore{}
	live := New("thread", store, nil, func(messages []*schema.Message) int { return len(messages) * 3 })
	err := live.AddHistory(ctx, "run", schema.UserMessage("input"))
	if err != nil {
		t.Fatal(err)
	}
	live.RecordModelUsage(ctx, &model.TokenUsage{TotalTokens: 410})
	snapshot := live.SnapshotContext()
	err = live.AddHistory(ctx, "run", schema.UserMessage("newer"))
	if err != nil {
		t.Fatal(err)
	}
	restored := New("thread", store, nil, func(messages []*schema.Message) int { return len(messages) * 3 })
	err = restored.ReloadHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := restored.ContextUsage()
	err = restored.RestoreContext(ctx, snapshot)
	if err != nil || restored.ContextUsage() != before {
		t.Fatalf("stale snapshot replaced newer context: %+v %v", restored.ContextUsage(), err)
	}
	snapshot.HistoryCursor = 3
	err = restored.RestoreContext(ctx, snapshot)
	if err == nil || restored.ContextUsage() != before {
		t.Fatalf("checkpoint ahead accepted or changed usage: %+v %v", restored.ContextUsage(), err)
	}
	snapshot.HistoryCursor = 2
	snapshot.Usage.CurrentTotal = -1
	err = restored.RestoreContext(ctx, snapshot)
	if err == nil || restored.ContextUsage() != before {
		t.Fatal("invalid snapshot changed usage")
	}
}
