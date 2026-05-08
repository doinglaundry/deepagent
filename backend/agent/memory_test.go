package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	memorystore "eino-cli/backend/memory/store"
)

// newSeededStore creates a memory store under a tmp dir and writes the
// given memories. Returns the store + the dir for cleanup.
func newSeededStore(t *testing.T, items ...memorystore.Memory) *memorystore.Store {
	t.Helper()
	store := memorystore.NewStore(t.TempDir())
	for _, m := range items {
		if err := store.Save(m); err != nil {
			t.Fatalf("seed store: %v", err)
		}
	}
	return store
}

// TestMemoryAccessor_PromptHooksRoundTrip verifies the prompt-side
// accessors return a typed payload that round-trips through the
// FormatMemoryForInjection bullet renderer.
func TestMemoryAccessor_PromptHooksRoundTrip(t *testing.T) {
	store := newSeededStore(t,
		memorystore.Memory{Key: "m1", Content: "user prefers tabs over spaces", TurnIndex: 1},
		memorystore.Memory{Key: "m2", Content: "user lives in UTC+8", TurnIndex: 2},
	)
	acc := NewMemoryAccessor(store)
	data := acc.GetMemories("any", "")
	out := acc.FormatMemoryForInjection(data, 0)

	if !strings.Contains(out, "user prefers tabs over spaces") {
		t.Errorf("expected first memory in output, got %q", out)
	}
	if !strings.Contains(out, "user lives in UTC+8") {
		t.Errorf("expected second memory in output, got %q", out)
	}
}

// TestMemoryAccessor_FilterDropsShortAndTaskPrefixed verifies
// MinContentLen and TaskMemoryPrefix exclusion both apply.
func TestMemoryAccessor_FilterDropsShortAndTaskPrefixed(t *testing.T) {
	store := newSeededStore(t,
		memorystore.Memory{Key: "ok", Content: "real preference about UTF-8", TurnIndex: 1},
		memorystore.Memory{Key: "tiny", Content: "hi", TurnIndex: 2},
		memorystore.Memory{Key: "task", Content: memorystore.TaskMemoryPrefix + "do x", TurnIndex: 3},
	)
	acc := NewMemoryAccessor(store)
	data := acc.GetMemories("any", "").(memoryDataKey)
	if len(data.Memories) != 1 {
		t.Fatalf("expected 1 memory after filter, got %d: %+v", len(data.Memories), data.Memories)
	}
	if data.Memories[0].Key != "ok" {
		t.Errorf("expected the long entry to survive, got %s", data.Memories[0].Key)
	}
}

// TestMemoryAccessor_InjectPrependsSystemMessage verifies the runtime
// hook prepends a <memory>...</memory> system block when the store is
// non-empty.
func TestMemoryAccessor_InjectPrependsSystemMessage(t *testing.T) {
	store := newSeededStore(t,
		memorystore.Memory{Key: "m1", Content: "user prefers concise answers", TurnIndex: 1},
	)
	acc := NewMemoryAccessor(store)
	hooks := acc.Hooks()

	in := []*schema.Message{schema.UserMessage("hi")}
	out := hooks.Inject(context.Background(), in)
	if len(out) != 2 {
		t.Fatalf("expected 2 messages after inject, got %d", len(out))
	}
	if out[0].Role != schema.System {
		t.Errorf("expected first message to be System, got %s", out[0].Role)
	}
	if !strings.Contains(out[0].Content, "<memory>") || !strings.Contains(out[0].Content, "</memory>") {
		t.Errorf("expected memory tags in injected message, got %q", out[0].Content)
	}
	if !strings.Contains(out[0].Content, "user prefers concise answers") {
		t.Errorf("expected memory content in injected message, got %q", out[0].Content)
	}
}

// TestMemoryAccessor_InjectIsNoOpWhenEmpty ensures we don't waste tokens
// when the memory store is empty (no system message added).
func TestMemoryAccessor_InjectIsNoOpWhenEmpty(t *testing.T) {
	acc := NewMemoryAccessor(newSeededStore(t))
	in := []*schema.Message{schema.UserMessage("hi")}
	out := acc.Hooks().Inject(context.Background(), in)
	if len(out) != 1 {
		t.Errorf("expected no inject for empty store, got %d msgs", len(out))
	}
}

// TestMemoryAccessor_NilStoreIsNoOp confirms passing nil is safe so the
// runtime layer can degrade gracefully when MemoryDir doesn't exist.
func TestMemoryAccessor_NilStoreIsNoOp(t *testing.T) {
	acc := NewMemoryAccessor(nil)
	data := acc.GetMemories("a", "")
	if acc.FormatMemoryForInjection(data, 0) != "" {
		t.Errorf("expected empty format for nil store")
	}
	in := []*schema.Message{schema.UserMessage("hi")}
	if got := acc.Hooks().Inject(context.Background(), in); len(got) != 1 {
		t.Errorf("expected pass-through for nil store, got %d msgs", len(got))
	}
}

// TestMemoryAccessor_FlushBeforeSummarization is a smoke test for the
// memory_flush_hook plumbing — the call must succeed without error
// even when the store is nil, and must return nil for a real store too.
// The actual extraction policy is intentionally a stub today; this
// test just guards the contract so a future commit landing the
// extraction logic doesn't accidentally start returning errors.
func TestMemoryAccessor_FlushBeforeSummarization(t *testing.T) {
	t.Run("nil store", func(t *testing.T) {
		acc := NewMemoryAccessor(nil)
		err := acc.FlushBeforeSummarization(context.Background(),
			adk.ChatModelAgentState{}, adk.ChatModelAgentState{})
		if err != nil {
			t.Errorf("nil store should be a no-op, got err=%v", err)
		}
	})
	t.Run("real store, empty state", func(t *testing.T) {
		acc := NewMemoryAccessor(newSeededStore(t))
		before := adk.ChatModelAgentState{
			Messages: []*schema.Message{
				schema.UserMessage("foo"),
				schema.AssistantMessage("bar", nil),
			},
		}
		after := adk.ChatModelAgentState{
			Messages: []*schema.Message{
				schema.SystemMessage("(summary)"),
			},
		}
		if err := acc.FlushBeforeSummarization(context.Background(), before, after); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

// TestMemoryAccessor_FormatRespectsTokenBudget verifies the soft token
// budget truncates output at the boundary.
func TestMemoryAccessor_FormatRespectsTokenBudget(t *testing.T) {
	store := newSeededStore(t,
		memorystore.Memory{Key: "a", Content: strings.Repeat("xxxxxxxxxx", 5), TurnIndex: 1},
		memorystore.Memory{Key: "b", Content: strings.Repeat("yyyyyyyyyy", 5), TurnIndex: 2},
		memorystore.Memory{Key: "c", Content: strings.Repeat("zzzzzzzzzz", 5), TurnIndex: 3},
	)
	acc := NewMemoryAccessor(store)
	data := acc.GetMemories("any", "")
	// Budget of 20 tokens ≈ 80 chars: only ~1 entry fits.
	short := acc.FormatMemoryForInjection(data, 20)
	long := acc.FormatMemoryForInjection(data, 0)
	if len(short) >= len(long) {
		t.Errorf("expected token budget to truncate output: short=%d long=%d", len(short), len(long))
	}
}
