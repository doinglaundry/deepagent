package agent

import (
	"log/slog"
	"strings"
	"sync"

	"eino-cli/backend/agent/skills"
	"eino-cli/backend/config"
)

// BuildPromptDeps wires up PromptDeps from a fully-loaded config.Config
// and an optional MemoryAccessor. Mirrors the Python "compose every
// dependency at the edge" pattern; without this helper the runtime
// layer would have to know about every individual data source.
//
// Pass nil for mem if memory is not configured — the prompt's <memory>
// section will simply be empty (the template handles nil accessors
// gracefully).
//
// Skills are loaded eagerly on first call (then cached for the lifetime
// of the returned PromptDeps) so the on-disk scan happens once per REPL
// session instead of once per turn.
//
// Knobs we deliberately do NOT expose here (LoadAgentSoul,
// EffectiveUserID, custom SkillLoader): nobody currently overrides
// them, and YAGNI — re-introduce the options struct only when a real
// caller appears.
func BuildPromptDeps(cfg config.Config, mem *MemoryAccessor) *PromptDeps {
	deps := &PromptDeps{}
	if mem != nil {
		deps.GetMemoryData = mem.GetMemoryData
		deps.FormatMemoryForInjection = mem.FormatMemoryForInjection
	}

	paths := append([]string(nil), cfg.Skills.Paths...)
	deps.LoadSkills = makeCachedSkillLoader(paths, cfg.Skills.Enabled)

	if names := DeferredToolNamesFromConfig(cfg); names != nil {
		deps.GetDeferredRegistry = names
	}

	// Subagent description lookup: surface AgentConfig.Description so
	// the prompt's <available-subagents> section gets a one-liner per
	// configured agent. Without this hook the section silently skips
	// every entry that isn't built-in (the previous behaviour).
	if len(cfg.Agents) > 0 {
		agents := cfg.Agents
		deps.GetSubagentConfig = func(name string) *SubagentConfig {
			a, ok := agents[name]
			if !ok || strings.TrimSpace(a.Description) == "" {
				return nil
			}
			return &SubagentConfig{Description: a.Description}
		}
	}

	if len(cfg.ACP.Agents) > 0 {
		acp := make(map[string]any, len(cfg.ACP.Agents))
		for name, a := range cfg.ACP.Agents {
			acp[name] = map[string]any{"description": a.Description}
		}
		deps.GetACPAgents = func() map[string]any { return acp }
	}

	return deps
}

// DeferredToolNamesFromConfig returns a closure compatible with
// AgentDeps.DeferredToolNamesFunc so the DeferredTools middleware can
// filter the active tool set. Returns nil when no deferred tools are
// configured — callers should pass that directly through
// AgentDeps.DeferredToolNamesFunc to keep the middleware from being
// attached.
func DeferredToolNamesFromConfig(cfg config.Config) func() []string {
	if len(cfg.ToolSearch.Deferred) == 0 {
		return nil
	}
	names := make([]string, 0, len(cfg.ToolSearch.Deferred))
	for _, e := range cfg.ToolSearch.Deferred {
		names = append(names, e.Name)
	}
	return func() []string { return names }
}

// makeCachedSkillLoader returns a closure that scans the given paths
// once and caches the result for the lifetime of the closure.
// sync.Once guards concurrent first-touch (multiple agent invocations
// from the TUI / smoke runs share one process). The enabled map is
// applied at scan time so disabled skills never reach the prompt; an
// empty map means "use deerflow defaults" (every public/custom skill
// enabled).
func makeCachedSkillLoader(paths []string, enabled map[string]bool) func() []Skill {
	if len(paths) == 0 {
		return func() []Skill { return nil }
	}

	var (
		once   sync.Once
		cached []Skill
	)

	return func() []Skill {
		once.Do(func() {
			loaded, err := skills.LoadFromPaths(paths)
			if err != nil {
				slog.Warn("skills loader: scan failed", "err", err)
				return
			}
			cached = make([]Skill, 0, len(loaded))
			for _, s := range loaded {
				if !skills.IsEnabled(s.Name, s.Category, enabled) {
					continue
				}
				cached = append(cached, Skill{
					Name:        s.Name,
					Description: s.Description,
					Category:    s.Category,
					SkillFile:   s.SkillFile,
				})
			}
		})
		return cached
	}
}
