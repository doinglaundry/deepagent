package memory

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
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
	cleanup := config.SetRootDirForTest(t.TempDir())
	t.Cleanup(cleanup)
	store := memorystore.NewStore()
	return NewMemoryUpdater(store), store
}

func basicConversation() []*schema.Message {
	return []*schema.Message{
		schema.UserMessage("I prefer tabs over spaces."),
		schema.AssistantMessage("Got it.", nil),
	}
}

// Run skips when chatModel is nil / messages empty, and returns nil error so
// callers don't log noise.
func TestMemoryUpdater_RunSkipsWhenNoModelOrMessages(t *testing.T) {
	updater, store := newUpdaterAndStore(t)

	chat := &fakeChatModel{response: "{}"}

	cases := []struct {
		name string
		chat model.BaseChatModel
		msgs []*schema.Message
	}{
		{"nil chatModel", nil, basicConversation()},
		{"empty messages", chat, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := updater.Run(context.Background(), c.chat, "alice", c.msgs, false)
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

	chat := &fakeChatModel{response: "{}"}
	err := updater.Run(context.Background(), chat, "alice", basicConversation(), false)
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

	resp := mustMarshal(t, updatePayload{
		User: map[string]sectionUpdate{
			"workContext": {Summary: "Go backend dev", ShouldUpdate: true},
		},
	})
	chat := &fakeChatModel{response: resp}

	err := updater.Run(context.Background(), chat, "alice", basicConversation(), true)
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

	err := updater.Run(context.Background(), chat, "alice", basicConversation(), false)
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

	err := updater.Run(context.Background(), chat, "alice", basicConversation(), false)
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

	err := updater.Run(context.Background(), chat, "alice", basicConversation(), false)
	if err == nil {
		t.Fatalf("expected LLM error")
	}
	if !updater.lastRunAt.IsZero() {
		t.Errorf("lastRunAt must not advance on LLM failure, got %v", updater.lastRunAt)
	}
}

// Empty (or whitespace-only) LLM content is a planned skip, not a parse
// error: no error returned, lastRunAt does not advance (so next turn retries),
// and the store is left untouched.
func TestMemoryUpdater_EmptyResponseIsPlannedSkip(t *testing.T) {
	updater, store := newUpdaterAndStore(t)

	for _, resp := range []string{"", "   ", "\n\t\n"} {
		chat := &fakeChatModel{response: resp}
		err := updater.Run(context.Background(), chat, "alice", basicConversation(), false)
		if err != nil {
			t.Errorf("response=%q: empty content should be silent skip, got %v", resp, err)
		}
		if !updater.lastRunAt.IsZero() {
			t.Errorf("response=%q: lastRunAt must not advance, got %v", resp, updater.lastRunAt)
		}
		if chat.calls != 1 {
			t.Errorf("response=%q: LLM should still be called once, got %d", resp, chat.calls)
		}
		got, _ := store.Load("alice")
		if got.User.WorkContext.Summary != "" {
			t.Errorf("response=%q: store should be untouched, got %+v", resp, got)
		}
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
	out := applyUpdate(current, upd)
	if out.User.WorkContext.Summary != "old" {
		t.Errorf("shouldUpdate=false should not overwrite, got %q", out.User.WorkContext.Summary)
	}
}

func TestApplyUpdate_NewFactBelowThresholdDropped(t *testing.T) {
	current := memorystore.GetEmptyMemoryData()

	upd := updatePayload{
		NewFacts: []factUpdate{
			{Content: "low", Category: "preference", Confidence: 0.5},
			{Content: "high", Category: "preference", Confidence: 0.9},
		},
	}
	out := applyUpdate(current, upd)
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

	out := applyUpdate(current, upd)
	if len(out.Facts) != 1 || out.Facts[0].ID != "fact_keep" {
		t.Errorf("factsToRemove did not drop expected id: %+v", out.Facts)
	}
}

func TestApplyUpdate_MaxFactsKeepsHighestConfidence(t *testing.T) {
	current := memorystore.GetEmptyMemoryData()
	for i := range 101 {
		current.Facts = append(current.Facts, memorystore.Fact{
			ID:         string(rune('a' + i)),
			Content:    string(rune('a' + i)),
			Confidence: float64(i) / 100,
		})
	}

	out := applyUpdate(current, updatePayload{})
	if len(out.Facts) != 100 {
		t.Fatalf("default max facts should cap to 100, got %d", len(out.Facts))
	}
	if !slices.ContainsFunc(out.Facts, func(f memorystore.Fact) bool { return f.Content == string(rune('a'+100)) }) {
		t.Errorf("max facts should keep highest confidence fact")
	}
}

func TestApplyUpdate_DefaultCategoryAndCorrectionSourceError(t *testing.T) {
	current := memorystore.GetEmptyMemoryData()

	upd := updatePayload{
		NewFacts: []factUpdate{
			{Content: "no category", Confidence: 0.9},
			{Content: "use spaces", Category: "correction", Confidence: 0.95, SourceError: "tabs broke build"},
		},
	}
	out := applyUpdate(current, upd)
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

func TestNormalizeFactContent(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"User Loves Go", "user loves go"},
		{"  User   Loves   Go  ", "user loves go"},
		{"USER LOVES GO", "user loves go"},
		{"\tUser\nLoves\tGo", "user loves go"},
		{"用户对 Git 感兴趣", "用户对 git 感兴趣"},
	}
	for _, c := range cases {
		got := normalizeFactContent(c.in)
		if got != c.want {
			t.Errorf("normalizeFactContent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestApplyUpdate_DedupMergesIdenticalContent(t *testing.T) {
	current := memorystore.GetEmptyMemoryData()
	current.Facts = []memorystore.Fact{
		{ID: "fact_old", Content: "用户对 Git 感兴趣", Confidence: 0.80, Kind: memorystore.FactKindEnduring},
	}

	upd := updatePayload{
		NewFacts: []factUpdate{
			{Content: "用户对 git 感兴趣", Category: "knowledge", Confidence: 0.70},
		},
	}
	out := applyUpdate(current, upd)
	if len(out.Facts) != 1 {
		t.Fatalf("expected dedup → 1 fact, got %d", len(out.Facts))
	}
	want := 0.85
	if got := out.Facts[0].Confidence; got < want-1e-6 || got > want+1e-6 {
		t.Errorf("merged confidence = %v, want %v (max(0.80,0.70)+0.05)", got, want)
	}
}

func TestApplyUpdate_DedupRespectsCeiling(t *testing.T) {
	current := memorystore.GetEmptyMemoryData()
	current.Facts = []memorystore.Fact{
		{ID: "fact_high", Content: "x", Confidence: 0.97, Kind: memorystore.FactKindEnduring},
	}

	upd := updatePayload{
		NewFacts: []factUpdate{{Content: "x", Confidence: 0.96}},
	}
	out := applyUpdate(current, upd)
	if len(out.Facts) != 1 {
		t.Fatalf("expected 1 merged fact, got %d", len(out.Facts))
	}
	if out.Facts[0].Confidence > 0.99 {
		t.Errorf("merged confidence %v exceeds 0.99 cap", out.Facts[0].Confidence)
	}
}

func TestApplyUpdate_KindDefaultsToEnduring(t *testing.T) {
	current := memorystore.GetEmptyMemoryData()

	upd := updatePayload{
		NewFacts: []factUpdate{{Content: "no kind", Confidence: 0.9}},
	}
	out := applyUpdate(current, upd)
	if len(out.Facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(out.Facts))
	}
	if out.Facts[0].Kind != memorystore.FactKindEnduring {
		t.Errorf("missing kind should default to %q, got %q", memorystore.FactKindEnduring, out.Facts[0].Kind)
	}
	if out.Facts[0].ExpiresAt != "" {
		t.Errorf("enduring fact must not get ExpiresAt, got %q", out.Facts[0].ExpiresAt)
	}
}

func TestApplyUpdate_EpisodicTTLBackfilled(t *testing.T) {
	current := memorystore.GetEmptyMemoryData()

	upd := updatePayload{
		NewFacts: []factUpdate{
			{Content: "find changelog line count", Confidence: 0.85, Kind: memorystore.FactKindEpisodic},
		},
	}
	out := applyUpdate(current, upd)
	if len(out.Facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(out.Facts))
	}
	if out.Facts[0].Kind != memorystore.FactKindEpisodic {
		t.Errorf("kind not preserved: %q", out.Facts[0].Kind)
	}
	if out.Facts[0].ExpiresAt == "" {
		t.Fatalf("episodic should have backfilled ExpiresAt")
	}

	parsed, err := time.Parse("2006-01-02T15:04:05Z", out.Facts[0].ExpiresAt)
	if err != nil {
		t.Fatalf("ExpiresAt not ISO-8601: %q (%v)", out.Facts[0].ExpiresAt, err)
	}
	delta := time.Until(parsed)
	if delta < 29*24*time.Hour || delta > 31*24*time.Hour {
		t.Errorf("ExpiresAt = now+%v, want ≈ now+30d", delta)
	}
}

func TestApplyUpdate_SweepRemovesExpiredEpisodic(t *testing.T) {
	current := memorystore.GetEmptyMemoryData()
	current.Facts = []memorystore.Fact{
		{ID: "keep_enduring", Content: "long-term", Confidence: 0.9, Kind: memorystore.FactKindEnduring},
		{ID: "drop_expired", Content: "old episodic", Confidence: 0.9, Kind: memorystore.FactKindEpisodic, ExpiresAt: "2020-01-01T00:00:00Z"},
		{ID: "keep_future", Content: "fresh episodic", Confidence: 0.9, Kind: memorystore.FactKindEpisodic, ExpiresAt: "2099-01-01T00:00:00Z"},
	}

	out := applyUpdate(current, updatePayload{})
	if len(out.Facts) != 2 {
		t.Fatalf("expected sweep to drop 1 expired episodic, got %d facts", len(out.Facts))
	}
	for _, f := range out.Facts {
		if f.ID == "drop_expired" {
			t.Errorf("expired episodic still present: %+v", f)
		}
	}
}
