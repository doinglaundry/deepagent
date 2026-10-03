package memory

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestService_OneInstanceKeepsScopesSeparate(t *testing.T) {
	for _, shared := range []bool{false, true} {
		config := Config{Root: t.TempDir(), Model: &memoryModel{}, Consolidator: func(_ context.Context, _ string, sources string) (string, error) { return sources, nil }}
		if shared {
			config.Store = newMemoryStore()
		}
		var service Service
		service, err := New(config)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		observeErr := service.Observe(ctx, "user/one", "same-thread", []*schema.Message{schema.UserMessage("fact")})
		if observeErr != nil {
			t.Fatal(observeErr)
		}
		consolidateErr := service.Consolidate(ctx, "user/one")
		if consolidateErr != nil {
			t.Fatal(consolidateErr)
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
		reloadedSnapshot, err := service.Read(ctx, "user/one")
		if err != nil || reloadedSnapshot.Summary == first.Summary {
			t.Fatalf("snapshot mutated stored memory: %+v %v", reloadedSnapshot, err)
		}
		for _, scope := range []string{"", " ", "user/one/"} {
			_, err := service.Read(ctx, scope)
			if err == nil {
				t.Fatalf("invalid scope accepted: %q", scope)
			}
		}
	}
}
