package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"eino-cli/backend/agent/middlewares"
	memorystore "eino-cli/backend/memory/store"
)

// memoryDataKey is the type the prompt-side accessor returns from
// GetMemoryData. We declare a named struct so the matching
// FormatMemoryForInjection can rely on a single shape — Python uses a
// dict; Go is happier with a typed payload.
type memoryDataKey struct {
	Memories []memorystore.Memory
}

// MemoryAccessor binds a memory/store.Store to the prompt-side and
// runtime-side memory hooks the lead agent expects. One accessor
// instance backs both:
//
//   - GetMemoryData / FormatMemoryForInjection → prompt template
//   - MemoryHooks{Inject, Extract}             → runtime middleware
//
// Both surfaces share the same store and the same de-duplication policy
// so a piece of context written in one place is reflected in the other.
type MemoryAccessor struct {
	store *memorystore.Store

	// MaxItems caps the number of memories injected per turn — a soft
	// guard against runaway prompts when the store grows large. 0 means
	// "no cap"; negative is treated as 0.
	MaxItems int

	// MinContentLen filters out trivial entries (matches the policy in
	// repl.prepareRoute, but enforced again here in case other writers
	// don't honour it).
	MinContentLen int

	mu sync.Mutex
}

// NewMemoryAccessor binds a memory store to the prompt + runtime hooks.
// Pass nil for store to get a no-op accessor (returns empty memory and
// silently skips writes).
func NewMemoryAccessor(store *memorystore.Store) *MemoryAccessor {
	return &MemoryAccessor{
		store:         store,
		MaxItems:      32,
		MinContentLen: 8,
	}
}

// GetMemoryData satisfies PromptDepsOptions.GetMemoryData. The agentName
// + userID arguments are accepted for API parity with deerflow but
// currently ignored — the store does not partition by either yet, and
// callers that need partitioning should provide a separate store.
func (a *MemoryAccessor) GetMemoryData(agentName, userID string) any {
	if a == nil || a.store == nil {
		return memoryDataKey{}
	}
	memories, err := a.store.LoadAll()
	if err != nil {
		// Match Python's "log + return empty" behaviour.
		promptLogger.Warn("memory accessor: load failed", "err", err)
		return memoryDataKey{}
	}
	cleaned := a.filter(memories)
	return memoryDataKey{Memories: cleaned}
}

// FormatMemoryForInjection turns the typed payload back into the bullet
// list deerflow's prompt template expects. The maxTokens parameter is a
// soft budget — we approximate "1 token ≈ 4 chars" rather than dragging
// in a tokenizer dep just for this. Callers can pass 0 to disable the
// budget.
func (a *MemoryAccessor) FormatMemoryForInjection(data any, maxTokens int) string {
	payload, ok := data.(memoryDataKey)
	if !ok || len(payload.Memories) == 0 {
		return ""
	}
	const charsPerToken = 4
	budget := 0
	if maxTokens > 0 {
		budget = maxTokens * charsPerToken
	}

	var (
		sb    strings.Builder
		used  int
		first = true
	)
	for _, m := range payload.Memories {
		line := fmt.Sprintf("- (turn %d) %s", m.TurnIndex, strings.TrimSpace(m.Content))
		if budget > 0 && used+len(line) > budget {
			break
		}
		if !first {
			sb.WriteByte('\n')
		}
		sb.WriteString(line)
		used += len(line) + 1
		first = false
	}
	return sb.String()
}

// Hooks returns the runtime-side MemoryHooks. Inject prepends a
// <memory>...</memory> system message on the first turn per Run; Extract
// is currently a no-op (the REPL writes new memories synchronously, so
// duplicating that work in a goroutine would only add contention).
func (a *MemoryAccessor) Hooks() middlewares.MemoryHooks {
	return middlewares.MemoryHooks{
		Inject:  a.inject,
		Extract: a.extract,
	}
}

func (a *MemoryAccessor) inject(_ context.Context, msgs []*schema.Message) []*schema.Message {
	if a == nil || a.store == nil {
		return msgs
	}
	memories, err := a.store.LoadAll()
	if err != nil {
		promptLogger.Warn("memory accessor: inject load failed", "err", err)
		return msgs
	}
	cleaned := a.filter(memories)
	if len(cleaned) == 0 {
		return msgs
	}

	bullets := make([]string, 0, len(cleaned))
	for _, m := range cleaned {
		bullets = append(bullets, "- "+strings.TrimSpace(m.Content))
	}
	block := "<memory>\n" + strings.Join(bullets, "\n") + "\n</memory>"

	out := make([]*schema.Message, 0, len(msgs)+1)
	out = append(out, schema.SystemMessage(block))
	out = append(out, msgs...)
	return out
}

func (a *MemoryAccessor) extract(_ context.Context, _ []*schema.Message) {
	// REPL-side writes already cover user inputs. Once a richer
	// "remember(key, value)" tool exists this is where the
	// extraction logic would land.
}

// FlushBeforeSummarization implements the deerflow-style
// `memory_flush_hook`: it is invoked by the summarization middleware
// after a summary has been finalised (see middlewares.NewSummarization
// — eino's Callback fires "after Finalize, before exiting the
// middleware", so technically the hook runs *just after* the
// summarization pivot rather than before).
//
// Today this is a logged stub: we don't have an LLM-driven memory
// extraction step yet, so there's nothing concrete to persist. The
// hook is plumbed end-to-end so a follow-up commit can drop in the
// extraction logic (scan `before.Messages` for facts / decisions, save
// them to the store) without re-touching the middleware chain.
//
// Errors from this hook are logged-and-swallowed by the wrapper in
// middlewares.NewSummarization — failing memory flush must never
// block summarization itself.
func (a *MemoryAccessor) FlushBeforeSummarization(
	ctx context.Context,
	before, after adk.ChatModelAgentState,
) error {
	if a == nil || a.store == nil {
		return nil
	}
	beforeCount := len(before.Messages)
	afterCount := len(after.Messages)
	dropped := beforeCount - afterCount
	if dropped < 0 {
		dropped = 0
	}
	promptLogger.Info(
		"memory flush hook fired",
		"before_messages", beforeCount,
		"after_messages", afterCount,
		"dropped", dropped,
	)
	// Future work: walk before.Messages and call a.store.Save on
	// any decision-grade items the agent surfaced. Keeping this a
	// stub keeps the plumbing visible to grep without speculating
	// on the extraction policy.
	return nil
}

// filter applies MinContentLen, dedupes by content, sorts by TurnIndex,
// and applies MaxItems. Returns a fresh slice ready for rendering.
func (a *MemoryAccessor) filter(in []memorystore.Memory) []memorystore.Memory {
	a.mu.Lock()
	maxItems, minLen := a.MaxItems, a.MinContentLen
	a.mu.Unlock()

	seen := make(map[string]struct{}, len(in))
	out := make([]memorystore.Memory, 0, len(in))
	for _, m := range in {
		c := strings.TrimSpace(m.Content)
		if c == "" {
			continue
		}
		if minLen > 0 && len([]rune(c)) < minLen {
			continue
		}
		if strings.HasPrefix(c, memorystore.TaskMemoryPrefix) {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, m)
	}
	// Stable order: oldest turns first.
	sort.SliceStable(out, func(i, j int) bool { return out[i].TurnIndex < out[j].TurnIndex })

	if maxItems > 0 && len(out) > maxItems {
		// Drop the oldest entries — keep the most recent context.
		out = out[len(out)-maxItems:]
	}
	return out
}
