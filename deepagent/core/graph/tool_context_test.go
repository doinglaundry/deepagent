package graph

import (
	"context"
	"testing"

	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	einotool "github.com/cloudwego/eino/components/tool"
)

type identityTool struct{ countingTool }

func (*identityTool) InvokableRun(ctx context.Context, _ string, _ ...einotool.Option) (string, error) {
	return GetToolCallID(ctx), nil
}

func TestToolExecutorExposesAssignedCallIdentity(t *testing.T) {
	ctx := context.Background()
	registry, err := tools.NewRegistry(ctx, []tools.Descriptor{{Tool: &identityTool{}, ParallelSafe: true}})
	if err != nil {
		t.Fatal(err)
	}
	executor := newToolExecutor("run", registry, 2, nil)
	results, err := executor.executeBatch(ctx, []types.ToolCall{
		{ID: "first", Index: 0, Name: "counter", Arguments: "{}"},
		{ID: "second", Index: 1, Name: "counter", Arguments: "{}"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Content != "first" || results[1].Content != "second" {
		t.Fatalf("incorrect tool context identities: %+v", results)
	}
	if id := GetToolCallID(ctx); id != "" {
		t.Fatalf("tool identity leaked into caller: %q", id)
	}
}
