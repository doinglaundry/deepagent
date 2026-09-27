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
func (t *toolSetTestTool) InvokableRun(_ context.Context, args string, _ ...einotool.Option) (string, error) {
	t.calls++
	return "result:" + args, nil
}

func TestToolSetPreservesSchemaAndExecution(t *testing.T) {
	ctx := context.Background()
	original := &toolSetTestTool{}
	r, err := NewToolSet(ctx, []ToolDescriptor{{Tool: original, ReadOnly: true}})
	if err != nil {
		t.Fatal(err)
	}
	infos, err := r.ModelTools(ctx)
	if err != nil || len(infos) != 1 || infos[0].Desc != "original" {
		t.Fatalf("infos=%v err=%v", infos, err)
	}
	d, ok := r.Lookup("read")
	if !ok {
		t.Fatal("execution lookup lost")
	}
	result, err := d.Tool.(einotool.InvokableTool).InvokableRun(ctx, "{}")
	if err != nil || result != "result:{}" || original.calls != 1 {
		t.Fatalf("result=%s calls=%d err=%v", result, original.calls, err)
	}
	originalInfo, _ := original.Info(ctx)
	if originalInfo.Desc != "original" {
		t.Fatal("mutated shared tool")
	}
}

func TestToolSetRejectsDuplicate(t *testing.T) {
	ctx := context.Background()
	if _, err := NewToolSet(ctx, []ToolDescriptor{{Tool: &toolSetTestTool{}}, {Tool: &toolSetTestTool{}}}); err == nil {
		t.Fatal("accepted duplicate tool name")
	}
}

func TestToolSetFilterKeepsExecutionAndOrder(t *testing.T) {
	ctx := context.Background()
	r, err := NewToolSet(ctx, []ToolDescriptor{{Tool: &toolSetTestTool{}, ReadOnly: true}})
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := r.Filter(ctx, true, func(_ context.Context, info *schema.ToolInfo) bool {
		info.Desc = "mask mutation"
		return info.Name == "read"
	})
	if err != nil {
		t.Fatal(err)
	}
	infos, err := filtered.ModelTools(ctx)
	if err != nil || len(infos) != 1 || infos[0].Desc != "original" {
		t.Fatalf("mask mutated stored schema: infos=%v err=%v", infos, err)
	}
	d, ok := filtered.Lookup("read")
	if !ok || !d.ReadOnly {
		t.Fatal("lost descriptor")
	}
	excluded, err := r.Filter(ctx, false, func(context.Context, *schema.ToolInfo) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := excluded.Lookup("read"); ok {
		t.Fatal("masked tool executable")
	}
	if _, ok := r.Lookup("read"); !ok {
		t.Fatal("filter mutated source")
	}
}
