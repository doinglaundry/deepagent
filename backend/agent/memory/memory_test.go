package memory

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"eino-cli/backend/config"
	memorystore "eino-cli/backend/memory/store"
)

func newMemoryStore(t *testing.T) *memorystore.Store {
	t.Helper()
	cleanup := config.SetRootDirForTest(t.TempDir())
	t.Cleanup(cleanup)
	return memorystore.NewStore()
}

func seedStore(t *testing.T, agentName string, data memorystore.MemoryData) *memorystore.Store {
	t.Helper()
	s := newMemoryStore(t)
	err := s.Save(agentName, data)
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}
	return s
}

func TestGetMemoryPromptBlock_NilStore(t *testing.T) {
	if got := GetMemoryPromptBlock(nil, "alice", 0); got != "" {
		t.Errorf("nil store should yield empty block, got %q", got)
	}
}

func TestGetMemoryPromptBlock_EmptyData(t *testing.T) {
	s := newMemoryStore(t)
	if got := GetMemoryPromptBlock(s, "alice", 0); got != "" {
		t.Errorf("empty store should yield empty block, got %q", got)
	}
}

func TestGetMemoryPromptBlock_RendersUserAndFacts(t *testing.T) {
	data := memorystore.GetEmptyMemoryData()
	data.User.WorkContext = memorystore.Section{Summary: "Go backend dev"}
	data.Facts = []memorystore.Fact{
		{ID: "fact_1", Content: "prefers tabs", Category: "preference", Confidence: 0.95},
	}
	s := seedStore(t, "alice", data)

	got := GetMemoryPromptBlock(s, "alice", 0)
	if !strings.HasPrefix(got, "<memory>\n") || !strings.HasSuffix(got, "\n</memory>") {
		t.Fatalf("missing memory tags: %q", got)
	}
	if !strings.Contains(got, "User Context:") || !strings.Contains(got, "- Work: Go backend dev") {
		t.Errorf("user section missing in block: %q", got)
	}
	if !strings.Contains(got, "Facts:") || !strings.Contains(got, "prefers tabs") {
		t.Errorf("facts section missing in block: %q", got)
	}
}

func TestInjectMemory_AppendsSystemMessage(t *testing.T) {
	data := memorystore.GetEmptyMemoryData()
	data.User.WorkContext = memorystore.Section{Summary: "Go backend dev"}
	s := seedStore(t, "alice", data)

	in := []*schema.Message{schema.UserMessage("hi")}
	out := InjectMemory(s, "alice", in)

	if len(out) != 2 {
		t.Fatalf("expected 2 messages after inject, got %d", len(out))
	}
	memoryMsg := out[len(out)-1]
	if memoryMsg.Role != schema.System {
		t.Errorf("expected last message to be System, got %s", memoryMsg.Role)
	}
	if !strings.Contains(memoryMsg.Content, "<memory>") || !strings.Contains(memoryMsg.Content, "</memory>") {
		t.Errorf("expected memory tags in injected message, got %q", memoryMsg.Content)
	}
	if !strings.Contains(memoryMsg.Content, "Go backend dev") {
		t.Errorf("expected memory content in injected message, got %q", memoryMsg.Content)
	}
}

func TestInjectMemory_NoOpWhenEmpty(t *testing.T) {
	s := newMemoryStore(t)
	in := []*schema.Message{schema.UserMessage("hi")}
	out := InjectMemory(s, "alice", in)
	if len(out) != 1 {
		t.Errorf("expected pass-through for empty store, got %d", len(out))
	}
}

func TestFormatMemoryForInjection_TruncatesUnderBudget(t *testing.T) {
	data := memorystore.GetEmptyMemoryData()
	data.User.WorkContext = memorystore.Section{Summary: strings.Repeat("aaaa ", 50)}
	data.Facts = []memorystore.Fact{
		{ID: "f1", Content: strings.Repeat("xxxx ", 50), Category: "preference", Confidence: 0.9},
		{ID: "f2", Content: strings.Repeat("yyyy ", 50), Category: "preference", Confidence: 0.8},
		{ID: "f3", Content: strings.Repeat("zzzz ", 50), Category: "preference", Confidence: 0.7},
	}
	short := formatMemoryForInjection(data, 30)
	long := formatMemoryForInjection(data, 0)
	if len(short) >= len(long) {
		t.Errorf("expected token budget to truncate output: short=%d long=%d", len(short), len(long))
	}
}

func TestFormatMemoryForInjection_FactsSortedByConfidence(t *testing.T) {
	data := memorystore.GetEmptyMemoryData()
	data.Facts = []memorystore.Fact{
		{ID: "f1", Content: "low", Category: "preference", Confidence: 0.3},
		{ID: "f2", Content: "high", Category: "preference", Confidence: 0.9},
		{ID: "f3", Content: "mid", Category: "preference", Confidence: 0.6},
	}
	got := formatMemoryForInjection(data, 0)

	highIdx := strings.Index(got, "high")
	midIdx := strings.Index(got, "mid")
	lowIdx := strings.Index(got, "low")
	if highIdx < 0 || midIdx < 0 || lowIdx < 0 {
		t.Fatalf("missing facts in output: %q", got)
	}
	if !(highIdx < midIdx && midIdx < lowIdx) {
		t.Errorf("facts not in confidence order: high=%d mid=%d low=%d", highIdx, midIdx, lowIdx)
	}
}

func TestFormatMemoryForInjection_CorrectionWithSourceError(t *testing.T) {
	data := memorystore.GetEmptyMemoryData()
	data.Facts = []memorystore.Fact{
		{ID: "f1", Content: "use spaces", Category: "correction", Confidence: 0.95, SourceError: "tabs caused failure"},
	}
	got := formatMemoryForInjection(data, 0)
	if !strings.Contains(got, "(avoid: tabs caused failure)") {
		t.Errorf("correction with sourceError should include avoid clause, got %q", got)
	}
}

// Single-section render checks: empty sections must not surface a header at
// all, even when other sections are populated. Catches regressions where a
// renderer accidentally emits "User Context:\n" with no bullets.

func TestFormatMemoryForInjection_OnlyUserSection(t *testing.T) {
	data := memorystore.GetEmptyMemoryData()
	data.User.WorkContext = memorystore.Section{Summary: "Go backend dev"}

	got := formatMemoryForInjection(data, 0)

	if !strings.Contains(got, "User Context:") || !strings.Contains(got, "- Work: Go backend dev") {
		t.Errorf("user section missing or malformed: %q", got)
	}
	if strings.Contains(got, "History:") {
		t.Errorf("history header must not appear when history is empty: %q", got)
	}
	if strings.Contains(got, "Facts:") {
		t.Errorf("facts header must not appear when facts are empty: %q", got)
	}
	if strings.Contains(got, "- Personal:") || strings.Contains(got, "- Current Focus:") {
		t.Errorf("user sub-bullets for empty fields must not appear: %q", got)
	}
}

func TestFormatMemoryForInjection_OnlyHistorySection(t *testing.T) {
	data := memorystore.GetEmptyMemoryData()
	data.History.RecentMonths = memorystore.Section{Summary: "shipped a CLI rewrite"}

	got := formatMemoryForInjection(data, 0)

	if !strings.Contains(got, "History:") || !strings.Contains(got, "- Recent: shipped a CLI rewrite") {
		t.Errorf("history section missing or malformed: %q", got)
	}
	if strings.Contains(got, "User Context:") {
		t.Errorf("user header must not appear when user is empty: %q", got)
	}
	if strings.Contains(got, "Facts:") {
		t.Errorf("facts header must not appear when facts are empty: %q", got)
	}
	if strings.Contains(got, "- Earlier:") || strings.Contains(got, "- Background:") {
		t.Errorf("history sub-bullets for empty fields must not appear: %q", got)
	}
}

func TestFormatMemoryForInjection_OnlyFactsSection(t *testing.T) {
	data := memorystore.GetEmptyMemoryData()
	data.Facts = []memorystore.Fact{
		{ID: "f1", Content: "prefers tabs", Category: "preference", Confidence: 0.9},
	}

	got := formatMemoryForInjection(data, 0)

	if !strings.Contains(got, "Facts:") || !strings.Contains(got, "- [preference | 0.90] prefers tabs") {
		t.Errorf("facts section missing or malformed: %q", got)
	}
	if strings.Contains(got, "User Context:") {
		t.Errorf("user header must not appear when user is empty: %q", got)
	}
	if strings.Contains(got, "History:") {
		t.Errorf("history header must not appear when history is empty: %q", got)
	}
}

func TestFormatMemoryForInjection_FiltersExpiredEpisodic(t *testing.T) {
	data := memorystore.GetEmptyMemoryData()
	data.Facts = []memorystore.Fact{
		{ID: "live_enduring", Content: "long-term pref", Category: "preference", Confidence: 0.9, Kind: memorystore.FactKindEnduring},
		{ID: "live_future_ep", Content: "fresh task", Category: "goal", Confidence: 0.9, Kind: memorystore.FactKindEpisodic, ExpiresAt: "2099-01-01T00:00:00Z"},
		{ID: "dead_past_ep", Content: "stale task", Category: "goal", Confidence: 0.9, Kind: memorystore.FactKindEpisodic, ExpiresAt: "2020-01-01T00:00:00Z"},
	}

	got := formatMemoryForInjection(data, 0)
	if !strings.Contains(got, "long-term pref") {
		t.Errorf("enduring fact must render: %q", got)
	}
	if !strings.Contains(got, "fresh task") {
		t.Errorf("future episodic must render: %q", got)
	}
	if strings.Contains(got, "stale task") {
		t.Errorf("expired episodic must be filtered out: %q", got)
	}
}

func TestFormatMemoryForInjection_AllExpiredOmitsFactsHeader(t *testing.T) {
	data := memorystore.GetEmptyMemoryData()
	data.Facts = []memorystore.Fact{
		{ID: "dead", Content: "stale", Confidence: 0.9, Kind: memorystore.FactKindEpisodic, ExpiresAt: "2020-01-01T00:00:00Z"},
	}

	got := formatMemoryForInjection(data, 0)
	if strings.Contains(got, "Facts:") {
		t.Errorf("all-expired episodic should suppress Facts header: %q", got)
	}
}

func TestFormatMemoryForInjection_LegacyFactWithoutKindRenders(t *testing.T) {
	data := memorystore.GetEmptyMemoryData()
	data.Facts = []memorystore.Fact{
		{ID: "legacy", Content: "old fact, no kind", Category: "preference", Confidence: 0.9},
	}

	got := formatMemoryForInjection(data, 0)
	if !strings.Contains(got, "old fact, no kind") {
		t.Errorf("legacy fact without Kind must render (treated as enduring): %q", got)
	}
}
