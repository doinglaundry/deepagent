package memory

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestService_OneInstanceKeepsScopesSeparate(t *testing.T) {
	for _, shared := range []bool{false, true} {
		cfg := Config{Root: t.TempDir(), Model: &memoryModel{}, Consolidator: func(_ context.Context, _ string, sources string) (string, error) { return sources, nil }}
		if shared {
			cfg.Store = newMemoryStore()
		}
		var service Service
		service, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if err := service.Observe(ctx, "user/one", "same-thread", []*schema.Message{schema.UserMessage("fact")}); err != nil {
			t.Fatal(err)
		}
		if err := service.Consolidate(ctx, "user/one"); err != nil {
			t.Fatal(err)
		}
		first, err := service.Read(ctx, "user/one")
		if err != nil || first.Scope != "user/one" || first.Summary == "" || first.UpdatedAt.IsZero() {
			t.Fatalf("snapshot=%+v err=%v", first, err)
		}
		second, err := service.Read(ctx, "user/two")
		if err != nil || second.Scope != "user/two" || second.Summary != "" {
			t.Fatalf("scope leak: %+v %v", second, err)
		}
		first.Summary = "modified by caller"
		again, err := service.Read(ctx, "user/one")
		if err != nil || again.Summary == first.Summary {
			t.Fatalf("snapshot mutated stored memory: %+v %v", again, err)
		}
		for _, scope := range []string{"", " ", "user/one/"} {
			if _, err := service.Read(ctx, scope); err == nil {
				t.Fatalf("invalid scope accepted: %q", scope)
			}
		}
	}
}
