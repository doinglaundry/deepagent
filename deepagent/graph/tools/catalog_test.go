package tools

import (
	"context"
	"testing"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type toolSetTestTool struct{ calls int }

func (*toolSetTestTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "read", Desc: "original"}, nil
}
func (testTool *toolSetTestTool) InvokableRun(_ context.Context, arguments string, _ ...einotool.Option) (string, error) {
	testTool.calls++
	return "result:" + arguments, nil
}

func TestToolSetPreservesSchemaAndExecution(t *testing.T) {
	ctx := context.Background()
	originalTool := &toolSetTestTool{}
	toolSet, err := NewToolSet(ctx, []ToolDescriptor{{Tool: originalTool, ReadOnly: true}})
	if err != nil {
		t.Fatal(err)
	}
	toolInfos, err := toolSet.GetToolInfos(ctx)
	if err != nil || len(toolInfos) != 1 || toolInfos[0].Desc != "original" {
		t.Fatalf("infos=%v err=%v", toolInfos, err)
	}
	toolDescriptor, ok := toolSet.GetToolDescriptor("read")
	if !ok {
		t.Fatal("execution lookup lost")
	}
	result, err := toolDescriptor.Tool.(einotool.InvokableTool).InvokableRun(ctx, "{}")
	if err != nil || result != "result:{}" || originalTool.calls != 1 {
		t.Fatalf("result=%s calls=%d err=%v", result, originalTool.calls, err)
	}
	originalToolInfo, _ := originalTool.Info(ctx)
	if originalToolInfo.Desc != "original" {
		t.Fatal("mutated shared tool")
	}
}

func TestToolSetRejectsDuplicate(t *testing.T) {
	ctx := context.Background()
	_, err := NewToolSet(ctx, []ToolDescriptor{{Tool: &toolSetTestTool{}}, {Tool: &toolSetTestTool{}}})
	if err == nil {
		t.Fatal("accepted duplicate tool name")
	}
}

func TestToolSetFilterKeepsExecutionAndOrder(t *testing.T) {
	ctx := context.Background()
	toolSet, err := NewToolSet(ctx, []ToolDescriptor{{Tool: &toolSetTestTool{}, ReadOnly: true}})
	if err != nil {
		t.Fatal(err)
	}
	filteredToolSet, err := toolSet.FilterTools(ctx, true, func(_ context.Context, toolInfo *schema.ToolInfo) bool {
		toolInfo.Desc = "mask mutation"
		return toolInfo.Name == "read"
	})
	if err != nil {
		t.Fatal(err)
	}
	toolInfos, err := filteredToolSet.GetToolInfos(ctx)
	if err != nil || len(toolInfos) != 1 || toolInfos[0].Desc != "original" {
		t.Fatalf("mask mutated stored schema: infos=%v err=%v", toolInfos, err)
	}
	toolDescriptor, ok := filteredToolSet.GetToolDescriptor("read")
	if !ok || !toolDescriptor.ReadOnly {
		t.Fatal("lost descriptor")
	}
	excludedToolSet, err := toolSet.FilterTools(ctx, false, func(context.Context, *schema.ToolInfo) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	_, excludedToolExists := excludedToolSet.GetToolDescriptor("read")
	if excludedToolExists {
		t.Fatal("masked tool executable")
	}
	_, toolExists := toolSet.GetToolDescriptor("read")
	if !toolExists {
		t.Fatal("filter mutated source")
	}
}
