package agent

import (
	"log/slog"
	"sync"

	"eino-cli/backend/agent/skills"
	"eino-cli/backend/config"
)

// PromptDepsOptions bundles overrides for BuildPromptDeps. Pass empty for
// defaults derived purely from cfg.
type PromptDepsOptions struct {
	// SkillLoader, if non-nil, returns the live skill list. Defaults to
	// scanning cfg.Skills.Paths via skills.LoadFromPaths.
	SkillLoader func() []Skill

	// EffectiveUserID returns the user identity used to namespace memory.
	// Defaults to "" (== Python's empty user_id default).
	EffectiveUserID func() string

	// Memory hooks — wired only when AppConfig.Memory.Enabled is also true.
	GetMemoryData            func(agentName, userID string) any
	FormatMemoryForInjection func(data any, maxTokens int) string

	// AgentSoulLoader returns the SOUL.md content for the given agent.
	// Defaults to "" (no <soul> section).
	LoadAgentSoul func(agentName string) string
}

// BuildPromptDeps wires up PromptDeps from a fully-loaded config.Config plus
// optional overrides. Mirrors the Python "compose every dependency at the
// edge" pattern; without this helper the runtime layer would have to know
// about every individual data source.
//
// Skills are loaded eagerly on first call (then cached for the lifetime of
// the returned PromptDeps) so the on-disk scan happens once per REPL session
// instead of once per turn.
func BuildPromptDeps(cfg config.Config, opts PromptDepsOptions) *PromptDeps {
	deps := &PromptDeps{
		LoadAgentSoul:            opts.LoadAgentSoul,
		GetEffectiveUserID:       opts.EffectiveUserID,
		GetMemoryData:            opts.GetMemoryData,
		FormatMemoryForInjection: opts.FormatMemoryForInjection,
	}

	loader := opts.SkillLoader
	if loader == nil {
		paths := append([]string(nil), cfg.Skills.Paths...)
		loader = makeCachedSkillLoader(paths)
	}
	deps.LoadSkills = loader

	if len(cfg.ToolSearch.Deferred) > 0 {
		registry := make([]DeferredEntry, 0, len(cfg.ToolSearch.Deferred))
		for _, e := range cfg.ToolSearch.Deferred {
			registry = append(registry, DeferredEntry{Name: e.Name})
		}
		deps.GetDeferredRegistry = func() []DeferredEntry { return registry }
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

// BuildAppConfig projects schema.Config onto agent.AppConfig (the
// runtime-merged view used by the prompt + middleware chain). Defaults that
// the Python deerflow runtime applies through code (rather than YAML) are
// reproduced here so the prompt section gating stays consistent across the
// two implementations.
func BuildAppConfig(cfg config.Config) *AppConfig {
	app := &AppConfig{
		ToolSearch: ToolSearchConfig{Enabled: cfg.ToolSearch.Enabled},
	}
	return app
}

// DeferredToolNamesFromConfig returns a closure compatible with
// ChainOptions.DeferredToolNames so the DeferredTools middleware can filter
// the active tool set. Returns nil when no deferred tools are configured —
// callers should pass that directly through ChainOptions.DeferredToolNames
// to keep the middleware from being attached.
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

// makeCachedSkillLoader returns a closure that scans the given paths once
// and caches the result for the lifetime of the closure. Wrap this in a
// mutex-guarded sync.Once so concurrent first-touch from multiple REPL
// turns doesn't double-scan.
func makeCachedSkillLoader(paths []string) func() []Skill {
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
