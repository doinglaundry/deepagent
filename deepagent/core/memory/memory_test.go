package memory

import (
	"context"
	"eino-cli/manager"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"testing"
)

type memoryModel struct{ calls int }

func (m *memoryModel) Generate(_ context.Context, in []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.calls++
	if m.calls == 1 {
		return schema.AssistantMessage("User prefers Go. Project uses MySQL.", nil), nil
	}
	return schema.AssistantMessage("# Memory\n- User prefers Go.\n- Project uses MySQL.", nil), nil
}
func (m *memoryModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("unused")
}
func TestExtractionConsolidationAndRestartBaseline(t *testing.T) {
	ctx := context.Background()
	m := &memoryModel{}
	root := t.TempDir()
	p, e := New(Config{Root: root, Model: m, Consolidator: func(context.Context, string, string) (string, error) {
		m.calls++
		return "# Memory\n- User prefers Go.\n- Project uses MySQL.", nil
	}})
	if e != nil {
		t.Fatal(e)
	}
	messages := []*schema.Message{schema.UserMessage("Use Go and MySQL.")}
	if e = p.Observe(ctx, "session/thread", messages); e != nil {
		t.Fatal(e)
	}
	if e = p.Observe(ctx, "session/thread", messages); e != nil {
		t.Fatal(e)
	}
	if m.calls != 1 {
		t.Fatal("unchanged source extracted twice")
	}
	if e = p.Consolidate(ctx); e != nil {
		t.Fatal(e)
	}
	p2, e := New(Config{Root: root, Model: m, Consolidator: func(context.Context, string, string) (string, error) {
		m.calls++
		return "# Memory\n- User prefers Go.\n- Project uses MySQL.", nil
	}})
	if e != nil {
		t.Fatal(e)
	}
	if e = p2.Consolidate(ctx); e != nil {
		t.Fatal(e)
	}
	if m.calls != 2 {
		t.Fatal("unchanged extractions consolidated twice")
	}
	summary, e := p2.Read(ctx)
	if e != nil || summary != "# Memory\n- User prefers Go.\n- Project uses MySQL." {
		t.Fatal(summary, e)
	}
}

func TestDurableArtifactsResumeOnDifferentWorkerDirectory(t *testing.T) {
	ctx := context.Background()
	store := manager.NewMemory("ns")
	m := &memoryModel{}
	consolidate := func(context.Context, string, string) (string, error) { return "Shared durable memory", nil }
	p, e := New(Config{Root: t.TempDir(), Model: m, Consolidator: consolidate, Store: store, Scope: "user/u1"})
	if e != nil {
		t.Fatal(e)
	}
	if e = p.Observe(ctx, "thread1", []*schema.Message{schema.UserMessage("Go preference")}); e != nil {
		t.Fatal(e)
	}
	p2, e := New(Config{Root: t.TempDir(), Model: m, Consolidator: consolidate, Store: store, Scope: "user/u1"})
	if e != nil {
		t.Fatal(e)
	}
	if e = p2.Consolidate(ctx); e != nil {
		t.Fatal(e)
	}
	out, e := p.Read(ctx)
	if e != nil || out != "Shared durable memory" {
		t.Fatal(out, e)
	}
	p3, _ := New(Config{Root: t.TempDir(), Model: m, Consolidator: consolidate, Store: store, Scope: "user/other"})
	out, e = p3.Read(ctx)
	if e != nil || out != "" {
		t.Fatal("memory user scope leaked", out, e)
	}
}
