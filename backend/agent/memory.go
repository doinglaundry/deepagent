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

const (
	memoryMaxItems      = 32
	memoryMinContentLen = 8
)

type memoryDataKey struct {
	Memories []memorystore.Memory
}

type MemoryAccessor struct {
	store *memorystore.Store
}

func NewMemoryAccessor(store *memorystore.Store) *MemoryAccessor {
	return &MemoryAccessor{store: store}
}

func (a *MemoryAccessor) GetPromptBlock(agentName string, maxTokens int) string {
	bullets := renderMemoryBullets(a.loadFiltered(), maxTokens)
	if bullets == "" {
		return ""
	}
	return "<memory>\n" + bullets + "\n</memory>"
}

func (a *MemoryAccessor) FormatMemoryForInjection(data any, maxTokens int) string {
	payload, ok := data.(memoryDataKey)
	if !ok {
		return ""
	}
	return renderMemoryBullets(payload.Memories, maxTokens)
}

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

func (a *MemoryAccessor) Hooks() middlewares.MemoryHooks {
	return middlewares.MemoryHooks{
		Inject:  a.inject,
		Extract: a.extract,
	}
}

func (a *MemoryAccessor) inject(_ context.Context, msgs []*schema.Message) []*schema.Message {
	block := a.GetPromptBlock("", 0)
	if block == "" {
		return msgs
	}
	out := make([]*schema.Message, 0, len(msgs)+1)
	out = append(out, schema.SystemMessage(block))
	out = append(out, msgs...)
	return out
}

func (a *MemoryAccessor) extract(_ context.Context, _ []*schema.Message) {}

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
	return nil
}

// renderMemoryBullets renders memories as a `- (turn N) content` list within a soft token budget.
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

func filterMemories(in []memorystore.Memory) []memorystore.Memory {
	seen := make(map[string]any, len(in))
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
	sort.SliceStable(out, func(i, j int) bool { return out[i].TurnIndex < out[j].TurnIndex })

	if memoryMaxItems > 0 && len(out) > memoryMaxItems {
		out = out[len(out)-memoryMaxItems:]
	}
	return out
}
