package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"eino-cli/backend/config"
	memorystore "eino-cli/backend/memory/store"
)

// memoryUpdateTimeout caps a single LLM call so a hung backend doesn't hold
// the updater mutex forever. memoryFlushTimeout is the shorter cap used by
// /clear summarization flushes — UI must not stall.
const (
	memoryUpdateTimeout = 60 * time.Second
	memoryFlushTimeout  = 5 * time.Second
)

// MemoryUpdater serialises LLM-driven memory updates per (store, agent).
// chatModel/cfg/agentName flow in through Run so the same updater can be
// shared between the per-turn memory hook and the /clear flush hook.
type MemoryUpdater struct {
	store *memorystore.Store

	mu        sync.Mutex
	lastRunAt time.Time
}

func NewMemoryUpdater(store *memorystore.Store) *MemoryUpdater {
	return &MemoryUpdater{store: store}
}

// Run extracts memory updates from messages, asks chatModel to produce a
// JSON delta against the current store, applies the delta, and persists.
// force=true bypasses debounce and is what the /clear flush hook passes;
// force=false is the per-turn path. Returns nil for a planned skip
// (disabled / nothing to update / debounced) so callers don't log noise.
func (u *MemoryUpdater) Run(
	ctx context.Context,
	chatModel model.BaseChatModel,
	cfg config.Memory,
	agentName string,
	messages []*schema.Message,
	force bool,
) error {
	if !cfg.Enabled || chatModel == nil || u == nil || u.store == nil {
		return nil
	}
	if len(messages) == 0 {
		return nil
	}

	u.mu.Lock()
	defer u.mu.Unlock()

	if !force && cfg.DebounceSeconds > 0 {
		if time.Since(u.lastRunAt) < time.Duration(cfg.DebounceSeconds)*time.Second {
			return nil
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, memoryUpdateTimeout)
	defer cancel()

	current, err := u.store.Load(agentName)
	if err != nil {
		return fmt.Errorf("load memory: %w", err)
	}

	convo := formatConversationForUpdate(messages)
	if strings.TrimSpace(convo) == "" {
		return nil
	}

	prompt, err := buildUpdatePrompt(current, convo)
	if err != nil {
		return fmt.Errorf("build prompt: %w", err)
	}

	resp, err := chatModel.Generate(runCtx, []*schema.Message{schema.UserMessage(prompt)})
	if err != nil {
		return fmt.Errorf("memory llm: %w", err)
	}
	if resp == nil {
		return nil
	}
	// Empty content = LLM had nothing to add for this turn (or transient
	// blip). Same shape as the convo-empty branch above: planned skip, no
	// debounce advance, retry next turn. Avoids spurious "parse update:
	// unexpected end of JSON input" warnings.
	if strings.TrimSpace(resp.Content) == "" {
		return nil
	}

	payload, err := parseUpdatePayload(resp.Content)
	if err != nil {
		return fmt.Errorf("parse update: %w", err)
	}

	updated := applyUpdate(current, payload, cfg)

	err = u.store.Save(agentName, updated)
	if err != nil {
		return fmt.Errorf("save memory: %w", err)
	}

	u.lastRunAt = time.Now()
	return nil
}

// updatePayload is the LLM-emitted JSON shape; types live with the updater
// because the renderer never needs them.
type updatePayload struct {
	User          map[string]sectionUpdate `json:"user"`
	History       map[string]sectionUpdate `json:"history"`
	NewFacts      []factUpdate             `json:"newFacts"`
	FactsToRemove []string                 `json:"factsToRemove"`
}

type sectionUpdate struct {
	Summary      string `json:"summary"`
	ShouldUpdate bool   `json:"shouldUpdate"`
}

type factUpdate struct {
	Content     string  `json:"content"`
	Category    string  `json:"category"`
	Confidence  float64 `json:"confidence"`
	Kind        string  `json:"kind,omitempty"`
	ExpiresAt   string  `json:"expiresAt,omitempty"`
	SourceError string  `json:"sourceError,omitempty"`
}

// normalizeFactContent collapses trivial wording variants (case + leading
// / trailing / inner whitespace) so write-side dedup can merge duplicates
// like "用户对 Git 感兴趣" vs "用户对 git 感兴趣". Intentionally minimal:
// no punctuation / stopword stripping, to avoid merging facts that are
// only superficially similar.
func normalizeFactContent(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.Join(strings.Fields(s), " ")
}

// findDuplicateFact returns the index of the first fact whose normalized
// content matches; -1 when nothing matches. O(n); n stays well under 100
// per cfg.MaxFacts so an index is overkill.
func findDuplicateFact(facts []memorystore.Fact, normalized string) int {
	for i := range facts {
		if normalizeFactContent(facts[i].Content) == normalized {
			return i
		}
	}
	return -1
}

// parseUpdatePayload tolerates LLMs that wrap JSON in ```json ... ``` fences.
// Unmarshal failures bubble up as wrapped errors so Run can decide not to
// advance lastRunAt.
func parseUpdatePayload(raw string) (updatePayload, error) {
	text := strings.TrimSpace(raw)

	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		if len(lines) >= 2 {
			if lines[len(lines)-1] == "```" {
				lines = lines[1 : len(lines)-1]
			} else {
				lines = lines[1:]
			}
			text = strings.Join(lines, "\n")
		}
	}

	var p updatePayload
	err := json.Unmarshal([]byte(text), &p)
	if err != nil {
		return updatePayload{}, fmt.Errorf("unmarshal update payload: %w", err)
	}
	return p, nil
}

// applyUpdate is a pure function: same (current, upd, cfg) -> same output
// modulo NewFactID()/utcNowISO(). Keeping it pure makes the merge logic
// trivially testable; Run handles the I/O.
func applyUpdate(
	current memorystore.MemoryData,
	upd updatePayload,
	cfg config.Memory,
) memorystore.MemoryData {
	now := utcNowISO()
	out := current

	if s, ok := upd.User["workContext"]; ok && s.ShouldUpdate {
		out.User.WorkContext = memorystore.Section{Summary: s.Summary, UpdatedAt: now}
	}
	if s, ok := upd.User["personalContext"]; ok && s.ShouldUpdate {
		out.User.PersonalContext = memorystore.Section{Summary: s.Summary, UpdatedAt: now}
	}
	if s, ok := upd.User["topOfMind"]; ok && s.ShouldUpdate {
		out.User.TopOfMind = memorystore.Section{Summary: s.Summary, UpdatedAt: now}
	}

	if s, ok := upd.History["recentMonths"]; ok && s.ShouldUpdate {
		out.History.RecentMonths = memorystore.Section{Summary: s.Summary, UpdatedAt: now}
	}
	if s, ok := upd.History["earlierContext"]; ok && s.ShouldUpdate {
		out.History.EarlierContext = memorystore.Section{Summary: s.Summary, UpdatedAt: now}
	}
	if s, ok := upd.History["longTermBackground"]; ok && s.ShouldUpdate {
		out.History.LongTermBackground = memorystore.Section{Summary: s.Summary, UpdatedAt: now}
	}

	if len(upd.FactsToRemove) > 0 {
		toRemove := make(map[string]struct{}, len(upd.FactsToRemove))
		for _, id := range upd.FactsToRemove {
			toRemove[id] = struct{}{}
		}
		kept := make([]memorystore.Fact, 0, len(out.Facts))
		for _, f := range out.Facts {
			if _, drop := toRemove[f.ID]; drop {
				continue
			}
			kept = append(kept, f)
		}
		out.Facts = kept
	}

	for _, nf := range upd.NewFacts {
		content := strings.TrimSpace(nf.Content)
		if content == "" {
			continue
		}
		conf := memorystore.CoerceConfidence(nf.Confidence)
		if conf < cfg.FactConfidenceThreshold {
			continue
		}
		category := strings.TrimSpace(nf.Category)
		if category == "" {
			category = "context"
		}

		if cfg.DedupEnabled {
			idx := findDuplicateFact(out.Facts, normalizeFactContent(content))
			if idx >= 0 {
				merged := out.Facts[idx].Confidence
				if conf > merged {
					merged = conf
				}
				merged += 0.05
				if merged > 0.99 {
					merged = 0.99
				}
				out.Facts[idx].Confidence = merged
				continue
			}
		}

		kind := nf.Kind
		if kind != memorystore.FactKindEpisodic {
			kind = memorystore.FactKindEnduring
		}
		expiresAt := nf.ExpiresAt
		if kind == memorystore.FactKindEpisodic && expiresAt == "" && cfg.EpisodicDefaultTTLSeconds > 0 {
			ttl := time.Duration(cfg.EpisodicDefaultTTLSeconds) * time.Second
			expiresAt = time.Now().UTC().Add(ttl).Format("2006-01-02T15:04:05Z")
		}

		out.Facts = append(out.Facts, memorystore.Fact{
			ID:          memorystore.NewFactID(),
			Content:     content,
			Category:    category,
			Confidence:  conf,
			Kind:        kind,
			ExpiresAt:   expiresAt,
			SourceError: strings.TrimSpace(nf.SourceError),
			CreatedAt:   now,
			Source:      "llm",
		})
	}

	if cfg.MaxFacts > 0 && len(out.Facts) > cfg.MaxFacts {
		sort.SliceStable(out.Facts, func(i, j int) bool {
			return out.Facts[i].Confidence > out.Facts[j].Confidence
		})
		out.Facts = out.Facts[:cfg.MaxFacts]
	}

	live := make([]memorystore.Fact, 0, len(out.Facts))
	for _, f := range out.Facts {
		if !f.IsExpired(now) {
			live = append(live, f)
		}
	}
	out.Facts = live

	out.LastUpdated = now
	return out
}

// utcNowISO mirrors store.utcNowISO so the agent package doesn't need to
// reach into store internals just to stamp timestamps.
func utcNowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}
