package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"eino-cli/backend/agent/middlewares"
	memorystore "eino-cli/backend/memory/store"
)

// Soft caps applied when surfacing memory in the prompt block. They
// don't shrink the underlying store; they just limit what reaches the
// LLM per turn. Tightening these is safe; loosening risks runaway
// prompts when the store grows large.
const (
	memoryMaxItems      = 32
	memoryMinContentLen = 8
)

// memoryDataKey is the deerflow-parity payload returned by GetMemories.
// Python uses a dict; we keep a typed struct so the matching
// FormatMemoryForInjection has a stable shape to assert against.
type memoryDataKey struct {
	Memories []memorystore.Memory
}

// MemoryAccessor binds a memory/store.Store to the prompt-side and
// runtime-side memory hooks the lead agent expects. One accessor
// instance backs both:
//
//   - PromptBlock                              → unified <memory>...</memory>
//                                                 renderer (used by
//                                                 getMemoryContext for
//                                                 prompt assembly and by
//                                                 the inject hook for
//                                                 runtime turns)
//   - GetMemories / FormatMemoryForInjection   → deerflow-parity API
//                                                 (lower-level pieces;
//                                                 prefer PromptBlock for
//                                                 new code)
//   - MemoryHooks{Inject, Extract}             → runtime middleware
//
// All surfaces share the same store and the same dedup / size policy
// (memoryMaxItems / memoryMinContentLen package consts) so a piece of
// context written in one place is reflected in the other.
type MemoryAccessor struct {
	store *memorystore.Store
}

// NewMemoryAccessor binds a memory store to the prompt + runtime hooks.
// Pass nil for store to get a no-op accessor (returns empty memory and
// silently skips writes).
func NewMemoryAccessor(store *memorystore.Store) *MemoryAccessor {
	return &MemoryAccessor{store: store}
}

// PromptBlock is the single source of truth for the
// `<memory>...</memory>` system block. Both the prompt assembler
// (getMemoryContext) and the runtime memory hook (inject) call this so
// the LLM sees a uniform shape regardless of which surface produced
// the block.
//
// Returns "" when there's nothing to inject (nil/empty store, load
// error, or all entries filtered out); never returns just empty tags.
// agentName is currently ignored — present for deerflow API parity and
// future per-agent partitioning. maxTokens is a soft budget
// (1 token ≈ 4 chars); pass 0 to disable.
func (a *MemoryAccessor) PromptBlock(agentName string, maxTokens int) string {
	bullets := renderMemoryBullets(a.loadFiltered(), maxTokens)
	if bullets == "" {
		return ""
	}
	return "<memory>\n" + bullets + "\n</memory>"
}

// GetMemories preserved for deerflow API parity. agentName + userID
// are accepted but currently ignored — the store does not partition
// by either yet, and callers that need partitioning should provide a
// separate store. Prefer PromptBlock for new call sites.
func (a *MemoryAccessor) GetMemories(agentName, userID string) any {
	return memoryDataKey{Memories: a.loadFiltered()}
}

// FormatMemoryForInjection renders a memoryDataKey payload as a bullet
// list (no <memory> wrapping). Most callers should use PromptBlock,
// which combines GetMemories + this + the tag wrapping.
func (a *MemoryAccessor) FormatMemoryForInjection(data any, maxTokens int) string {
	payload, ok := data.(memoryDataKey)
	if !ok {
		return ""
	}
	return renderMemoryBullets(payload.Memories, maxTokens)
}

// loadFiltered loads from the store and applies the dedupe / size
// policy. Returns nil when the accessor or its store is unavailable
// (also nil on load error, which is logged once).
func (a *MemoryAccessor) loadFiltered() []memorystore.Memory {
	if a == nil || a.store == nil {
		return nil
	}
	memories, err := a.store.LoadAll()
	if err != nil {
		promptLogger.Warn("memory accessor: load failed", "err", err)
		return nil
	}
	return filterMemories(memories)
}

// Hooks returns the runtime-side MemoryHooks. Inject prepends a
// <memory>...</memory> system message on the first turn per Run via
// PromptBlock; Extract is currently a no-op (the REPL writes new
// memories synchronously, so duplicating that work in a goroutine
// would only add contention).
func (a *MemoryAccessor) Hooks() middlewares.MemoryHooks {
	return middlewares.MemoryHooks{
		Inject:  a.inject,
		Extract: a.extract,
	}
}

func (a *MemoryAccessor) inject(_ context.Context, msgs []*schema.Message) []*schema.Message {
	block := a.PromptBlock("", 0)
	if block == "" {
		return msgs
	}
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

// renderMemoryBullets renders memories as a `- (turn N) content` list
// honouring a soft token budget (charsPerToken=4). Returns "" for
// empty input or a budget that fits zero entries.
func renderMemoryBullets(memories []memorystore.Memory, maxTokens int) string {
	if len(memories) == 0 {
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
	for _, m := range memories {
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

// filterMemories applies trim, MinContentLen, TaskMemoryPrefix
// exclusion, content-dedupe, turn-asc sort, and the MaxItems cap. It's
// a free function (no accessor state) so the policy is the same
// regardless of which entry point pulled the slice.
func filterMemories(in []memorystore.Memory) []memorystore.Memory {
	seen := make(map[string]struct{}, len(in))
	out := make([]memorystore.Memory, 0, len(in))
	for _, m := range in {
		c := strings.TrimSpace(m.Content)
		if c == "" {
			continue
		}
		if memoryMinContentLen > 0 && len([]rune(c)) < memoryMinContentLen {
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

	if memoryMaxItems > 0 && len(out) > memoryMaxItems {
		// Drop the oldest entries — keep the most recent context.
		out = out[len(out)-memoryMaxItems:]
	}
	return out
}
