package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"eino-cli/backend/config"
	memorystore "eino-cli/backend/memory/store"
)

// fakeChatModel is a minimal model.BaseChatModel that returns a canned
// response on Generate. Stream is not exercised by the updater, but we have
// to implement it to satisfy the interface.
type fakeChatModel struct {
	response string
	err      error
	calls    int
}

func (f *fakeChatModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return schema.AssistantMessage(f.response, nil), nil
}

func (f *fakeChatModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("Stream not used by MemoryUpdater")
}

func newUpdaterAndStore(t *testing.T) (*MemoryUpdater, *memorystore.Store) {
	t.Helper()
	store := memorystore.NewStore(t.TempDir())
	return NewMemoryUpdater(store), store
}

func enabledMemoryCfg() config.Memory {
	return config.Memory{
		Enabled:                 true,
		InjectionEnabled:        true,
		MaxInjectionTokens:      0,
		DebounceSeconds:         0,
		MaxFacts:                10,
		FactConfidenceThreshold: 0.5,
	}
}

func basicConversation() []*schema.Message {
	return []*schema.Message{
		schema.UserMessage("I prefer tabs over spaces."),
		schema.AssistantMessage("Got it.", nil),
	}
}

// Run skips when memory is disabled / chatModel is nil / messages empty,
// and returns nil error so callers don't log noise.
func TestMemoryUpdater_RunSkipsWhenDisabled(t *testing.T) {
	updater, store := newUpdaterAndStore(t)

	chat := &fakeChatModel{response: "{}"}

	cases := []struct {
		name string
		cfg  config.Memory
		chat model.BaseChatModel
		msgs []*schema.Message
	}{
		{"disabled", config.Memory{Enabled: false}, chat, basicConversation()},
		{"nil chatModel", enabledMemoryCfg(), nil, basicConversation()},
		{"empty messages", enabledMemoryCfg(), chat, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := updater.Run(context.Background(), c.chat, c.cfg, "alice", c.msgs, false)
			if err != nil {
				t.Errorf("Run should be a no-op (no error), got %v", err)
			}
			if chat.calls != 0 {
				t.Errorf("LLM should not be called, got calls=%d", chat.calls)
			}
			if _, statErr := store.Load("alice"); statErr != nil {
				t.Errorf("store should be untouched, Load err=%v", statErr)
			}
		})
	}
}

func TestMemoryUpdater_DebounceSkips(t *testing.T) {
	updater, _ := newUpdaterAndStore(t)
	updater.lastRunAt = time.Now()

	cfg := enabledMemoryCfg()
	cfg.DebounceSeconds = 60

	chat := &fakeChatModel{response: "{}"}
	err := updater.Run(context.Background(), chat, cfg, "alice", basicConversation(), false)
	if err != nil {
		t.Fatalf("debounced Run should not error, got %v", err)
	}
	if chat.calls != 0 {
		t.Errorf("debounce should skip LLM call, got calls=%d", chat.calls)
	}
}

func TestMemoryUpdater_ForceBypassesDebounce(t *testing.T) {
	updater, store := newUpdaterAndStore(t)
	updater.lastRunAt = time.Now()

	cfg := enabledMemoryCfg()
	cfg.DebounceSeconds = 60

	resp := mustMarshal(t, updatePayload{
		User: map[string]sectionUpdate{
			"workContext": {Summary: "Go backend dev", ShouldUpdate: true},
		},
	})
	chat := &fakeChatModel{response: resp}

	err := updater.Run(context.Background(), chat, cfg, "alice", basicConversation(), true)
	if err != nil {
		t.Fatalf("force Run failed: %v", err)
	}
	if chat.calls != 1 {
		t.Errorf("force should bypass debounce, calls=%d", chat.calls)
	}
	got, _ := store.Load("alice")
	if got.User.WorkContext.Summary != "Go backend dev" {
		t.Errorf("section not applied: %+v", got.User.WorkContext)
	}
}

func TestMemoryUpdater_StripsCodeFenceAroundJSON(t *testing.T) {
	updater, store := newUpdaterAndStore(t)

	body := mustMarshal(t, updatePayload{
		User: map[string]sectionUpdate{
			"workContext": {Summary: "Go backend dev", ShouldUpdate: true},
		},
	})
	chat := &fakeChatModel{response: "```json\n" + body + "\n```"}

	err := updater.Run(context.Background(), chat, enabledMemoryCfg(), "alice", basicConversation(), false)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	got, _ := store.Load("alice")
	if got.User.WorkContext.Summary != "Go backend dev" {
		t.Errorf("fenced JSON not parsed; section unchanged: %+v", got.User.WorkContext)
	}
}

func TestMemoryUpdater_BadJSONReturnsErrAndDoesNotAdvance(t *testing.T) {
	updater, store := newUpdaterAndStore(t)
	chat := &fakeChatModel{response: "not json"}

	err := updater.Run(context.Background(), chat, enabledMemoryCfg(), "alice", basicConversation(), false)
	if err == nil {
		t.Fatalf("expected parse error")
	}
	if !updater.lastRunAt.IsZero() {
		t.Errorf("lastRunAt must not advance on parse failure, got %v", updater.lastRunAt)
	}
	got, _ := store.Load("alice")
	if got.User.WorkContext.Summary != "" {
		t.Errorf("store should be untouched on parse failure, got %+v", got)
	}
}

func TestMemoryUpdater_LLMErrorReturnsErrAndDoesNotAdvance(t *testing.T) {
	updater, _ := newUpdaterAndStore(t)
	chat := &fakeChatModel{err: errors.New("boom")}

	err := updater.Run(context.Background(), chat, enabledMemoryCfg(), "alice", basicConversation(), false)
	if err == nil {
		t.Fatalf("expected LLM error")
	}
	if !updater.lastRunAt.IsZero() {
		t.Errorf("lastRunAt must not advance on LLM failure, got %v", updater.lastRunAt)
	}
}

func TestApplyUpdate_ShouldUpdateFalseLeavesSectionAlone(t *testing.T) {
	current := memorystore.GetEmptyMemoryData()
	current.User.WorkContext = memorystore.Section{Summary: "old", UpdatedAt: "2026-01-01T00:00:00Z"}

	upd := updatePayload{
		User: map[string]sectionUpdate{
			"workContext": {Summary: "new", ShouldUpdate: false},
		},
	}
	out := applyUpdate(current, upd, enabledMemoryCfg())
	if out.User.WorkContext.Summary != "old" {
		t.Errorf("shouldUpdate=false should not overwrite, got %q", out.User.WorkContext.Summary)
	}
}

func TestApplyUpdate_NewFactBelowThresholdDropped(t *testing.T) {
	current := memorystore.GetEmptyMemoryData()

	cfg := enabledMemoryCfg()
	cfg.FactConfidenceThreshold = 0.7

	upd := updatePayload{
		NewFacts: []factUpdate{
			{Content: "low", Category: "preference", Confidence: 0.5},
			{Content: "high", Category: "preference", Confidence: 0.9},
		},
	}
	out := applyUpdate(current, upd, cfg)
	if len(out.Facts) != 1 || out.Facts[0].Content != "high" {
		t.Errorf("expected only high-confidence fact, got %+v", out.Facts)
	}
}

func TestApplyUpdate_FactsToRemoveByID(t *testing.T) {
	current := memorystore.GetEmptyMemoryData()
	current.Facts = []memorystore.Fact{
		{ID: "fact_keep", Content: "keep", Confidence: 0.9},
		{ID: "fact_drop", Content: "drop", Confidence: 0.9},
	}
	upd := updatePayload{FactsToRemove: []string{"fact_drop"}}

	out := applyUpdate(current, upd, enabledMemoryCfg())
	if len(out.Facts) != 1 || out.Facts[0].ID != "fact_keep" {
		t.Errorf("factsToRemove did not drop expected id: %+v", out.Facts)
	}
}

func TestApplyUpdate_MaxFactsKeepsHighestConfidence(t *testing.T) {
	current := memorystore.GetEmptyMemoryData()
	current.Facts = []memorystore.Fact{
		{ID: "fact_a", Content: "a", Confidence: 0.5},
		{ID: "fact_b", Content: "b", Confidence: 0.9},
		{ID: "fact_c", Content: "c", Confidence: 0.7},
	}
	cfg := enabledMemoryCfg()
	cfg.MaxFacts = 2

	out := applyUpdate(current, updatePayload{}, cfg)
	if len(out.Facts) != 2 {
		t.Fatalf("MaxFacts=2 should cap to 2, got %d", len(out.Facts))
	}
	got := []string{out.Facts[0].Content, out.Facts[1].Content}
	if !(contains(got, "b") && contains(got, "c")) {
		t.Errorf("MaxFacts should keep highest confidence (b,c), got %+v", got)
	}
}

func TestApplyUpdate_DefaultCategoryAndCorrectionSourceError(t *testing.T) {
	current := memorystore.GetEmptyMemoryData()
	cfg := enabledMemoryCfg()
	cfg.FactConfidenceThreshold = 0

	upd := updatePayload{
		NewFacts: []factUpdate{
			{Content: "no category", Confidence: 0.9},
			{Content: "use spaces", Category: "correction", Confidence: 0.95, SourceError: "tabs broke build"},
		},
	}
	out := applyUpdate(current, upd, cfg)
	if len(out.Facts) != 2 {
		t.Fatalf("expected 2 facts, got %d", len(out.Facts))
	}
	var got map[string]memorystore.Fact = make(map[string]memorystore.Fact, 2)
	for _, f := range out.Facts {
		got[f.Content] = f
	}
	if got["no category"].Category != "context" {
		t.Errorf("missing category should default to context, got %q", got["no category"].Category)
	}
	if got["use spaces"].SourceError != "tabs broke build" {
		t.Errorf("correction sourceError lost: %+v", got["use spaces"])
	}
	if got["use spaces"].Source != "llm" {
		t.Errorf("Source should be 'llm' for new facts, got %q", got["use spaces"].Source)
	}
}

func TestParseUpdatePayload_HandlesPartialFences(t *testing.T) {
	body := `{"user": {"workContext": {"summary": "x", "shouldUpdate": true}}}`
	cases := []string{
		body,
		"```json\n" + body + "\n```",
		"```\n" + body + "\n```",
		"```json\n" + body, // no closing fence; still parseable
	}
	for _, raw := range cases {
		got, err := parseUpdatePayload(raw)
		if err != nil {
			t.Errorf("parseUpdatePayload(%q): %v", raw, err)
			continue
		}
		s, ok := got.User["workContext"]
		if !ok || !s.ShouldUpdate || s.Summary != "x" {
			t.Errorf("unexpected payload from %q: %+v", raw, got)
		}
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func TestFormatConversationForUpdate_SkipsSystemAndTruncates(t *testing.T) {
	long := strings.Repeat("a", messageContentMaxLen+50)
	msgs := []*schema.Message{
		schema.SystemMessage("ignored"),
		schema.UserMessage(long),
		schema.AssistantMessage("short reply", nil),
	}
	out := formatConversationForUpdate(msgs)
	if strings.Contains(out, "ignored") {
		t.Errorf("system message should be skipped, got %q", out)
	}
	if !strings.Contains(out, "User: ") || !strings.Contains(out, "Assistant: short reply") {
		t.Errorf("missing expected role prefixes: %q", out)
	}
	if !strings.Contains(out, "...") {
		t.Errorf("long user message should be truncated with '...', got %q", out)
	}
}

func TestBuildUpdatePrompt_SubstitutesPlaceholders(t *testing.T) {
	current := memorystore.GetEmptyMemoryData()
	current.User.WorkContext = memorystore.Section{Summary: "go dev"}

	prompt, err := buildUpdatePrompt(current, "User: hello\n\nAssistant: hi")
	if err != nil {
		t.Fatalf("buildUpdatePrompt: %v", err)
	}
	if strings.Contains(prompt, "__CURRENT_MEMORY__") ||
		strings.Contains(prompt, "__CONVERSATION__") ||
		strings.Contains(prompt, "__CORRECTION_HINT__") {
		t.Errorf("placeholders not substituted: %q", prompt)
	}
	if !strings.Contains(prompt, "go dev") {
		t.Errorf("current memory not embedded")
	}
	if !strings.Contains(prompt, "User: hello") {
		t.Errorf("conversation not embedded")
	}
}
