package tools

import (
	"context"
	"errors"
	"testing"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type registryTestTool struct{ calls int }

func (*registryTestTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "read", Desc: "original"}, nil
}
func (t *registryTestTool) InvokableRun(_ context.Context, args string, _ ...einotool.Option) (string, error) {
	t.calls++
	return "result:" + args, nil
}

func TestTools_RewriteInfoPreservesExecution(t *testing.T) {
	ctx := context.Background()
	original := &registryTestTool{}
	r, err := NewRegistry(ctx, []Descriptor{{Tool: original, ReadOnly: true}})
	if err != nil {
		t.Fatal(err)
	}
	err = r.RewriteInfo(ctx, func(_ context.Context, info *schema.ToolInfo) (*schema.ToolInfo, error) {
		info.Desc = "rewritten"
		return info, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	infos, err := r.ModelTools(ctx)
	if err != nil || len(infos) != 1 || infos[0].Desc != "rewritten" {
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

func TestRegistryRejectsDuplicateAndInvalidRewrite(t *testing.T) {
	ctx := context.Background()
	if _, err := NewRegistry(ctx, []Descriptor{{Tool: &registryTestTool{}}, {Tool: &registryTestTool{}}}); err == nil {
		t.Fatal("accepted duplicate tool name")
	}
	r, err := NewRegistry(ctx, []Descriptor{{Tool: &registryTestTool{}}})
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("rewrite failure")
	if err := r.RewriteInfo(ctx, func(context.Context, *schema.ToolInfo) (*schema.ToolInfo, error) { return nil, sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	infos, _ := r.ModelTools(ctx)
	if infos[0].Name != "read" || infos[0].Desc != "original" {
		t.Fatal("failed rewrite changed registry")
	}
	if err := r.RewriteInfo(ctx, func(_ context.Context, i *schema.ToolInfo) (*schema.ToolInfo, error) {
		i.Name = "renamed"
		return i, nil
	}); err != nil {
		t.Fatal(err)
	}
	descriptor, ok := r.Lookup("renamed")
	if !ok {
		t.Fatal("rewritten name lost execution mapping")
	}
	result, err := descriptor.Tool.(einotool.InvokableTool).InvokableRun(ctx, "renamed input")
	if err != nil || result != "result:renamed input" {
		t.Fatalf("renamed tool result=%q err=%v", result, err)
	}
	if _, ok := r.Lookup("read"); ok {
		t.Fatal("old name remains executable after rename")
	}
}

func TestRegistryFilterKeepsExecutionAndOrder(t *testing.T) {
	ctx := context.Background()
	r, err := NewRegistry(ctx, []Descriptor{{Tool: &registryTestTool{}, ReadOnly: true}})
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := r.Filter(ctx, true, func(_ context.Context, info *schema.ToolInfo) bool { return info.Name == "read" })
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := filtered.Lookup("read"); !ok || !d.ReadOnly {
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
