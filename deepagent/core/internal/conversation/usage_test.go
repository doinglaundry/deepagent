package conversation

import (
	"context"
	"testing"

	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestContext_CumulativeUsageSurvivesCompaction(t *testing.T) {
	ctx := context.Background()
	c := New("thread", &testStore{}, &testCompactor{}, nil)
	if err := c.AddHistory(ctx, "run", schema.UserMessage("old"), schema.AssistantMessage("done", nil), schema.UserMessage("new")); err != nil {
		t.Fatal(err)
	}
	c.RecordModelUsage(ctx, &model.TokenUsage{PromptTokens: 3, CompletionTokens: 2})
	if _, err := c.Compact(ctx, "run"); err != nil {
		t.Fatal(err)
	}
	c.RecordModelUsage(ctx, nil)
	c.RecordModelUsage(ctx, &model.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5})
	if got := c.RunUsage(); got != (types.Usage{PromptTokens: 6, CompletionTokens: 4, TotalTokens: 10}) {
		t.Fatalf("cumulative=%+v", got)
	}
	if got := c.ContextUsage(); got.CurrentTotal != 5 {
		t.Fatalf("context estimate=%+v", got)
	}
	if err := c.RestoreRunUsage(ctx, types.Usage{TotalTokens: -1}); err == nil {
		t.Fatal("negative checkpoint usage accepted")
	}
	if c.RunUsage().TotalTokens != 10 {
		t.Fatal("invalid snapshot modified tracker")
	}
	if err := c.RestoreRunUsage(ctx, types.Usage{}); err != nil {
		t.Fatal(err)
	}
	if c.RunUsage().TotalTokens != 0 || c.ContextUsage().CurrentTotal != 5 {
		t.Fatal("new run must reset only cumulative usage")
	}
}
