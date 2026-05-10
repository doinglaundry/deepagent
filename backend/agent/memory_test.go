package agent

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"eino-cli/backend/config"
	memorystore "eino-cli/backend/memory/store"
)

func seedStore(t *testing.T, agentName string, data memorystore.MemoryData) *memorystore.Store {
	t.Helper()
	s := memorystore.NewStore(t.TempDir())
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
	s := memorystore.NewStore(t.TempDir())
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

func TestInjectMemory_DisabledByConfig(t *testing.T) {
	data := memorystore.GetEmptyMemoryData()
	data.User.WorkContext = memorystore.Section{Summary: "stuff"}
	s := seedStore(t, "alice", data)

	in := []*schema.Message{schema.UserMessage("hi")}

	cases := []config.Memory{
		{Enabled: false, InjectionEnabled: true},
		{Enabled: true, InjectionEnabled: false},
	}
	for _, cfg := range cases {
		out := InjectMemory(s, cfg, "alice", in)
		if len(out) != 1 || out[0].Role != schema.User {
			t.Errorf("InjectMemory should pass through when disabled (%+v), got %d msgs", cfg, len(out))
		}
	}
}

func TestInjectMemory_PrependsSystemMessage(t *testing.T) {
	data := memorystore.GetEmptyMemoryData()
	data.User.WorkContext = memorystore.Section{Summary: "Go backend dev"}
	s := seedStore(t, "alice", data)

	cfg := config.Memory{Enabled: true, InjectionEnabled: true, MaxInjectionTokens: 0}
	in := []*schema.Message{schema.UserMessage("hi")}
	out := InjectMemory(s, cfg, "alice", in)

	if len(out) != 2 {
		t.Fatalf("expected 2 messages after inject, got %d", len(out))
	}
	if out[0].Role != schema.System {
		t.Errorf("expected first message to be System, got %s", out[0].Role)
	}
	if !strings.Contains(out[0].Content, "<memory>") || !strings.Contains(out[0].Content, "</memory>") {
		t.Errorf("expected memory tags in injected message, got %q", out[0].Content)
	}
	if !strings.Contains(out[0].Content, "Go backend dev") {
		t.Errorf("expected memory content in injected message, got %q", out[0].Content)
	}
}

func TestInjectMemory_NoOpWhenEmpty(t *testing.T) {
	s := memorystore.NewStore(t.TempDir())
	cfg := config.Memory{Enabled: true, InjectionEnabled: true}
	in := []*schema.Message{schema.UserMessage("hi")}
	out := InjectMemory(s, cfg, "alice", in)
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
