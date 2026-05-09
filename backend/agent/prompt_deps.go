package agent

import (
	"log/slog"

	"eino-cli/backend/agent/skills"
	"eino-cli/backend/config"
)

// This file holds the small, stateless helpers that ApplyPromptTemplate
// (and the runtime middleware factory) consult to derive prompt-time
// data from a config.Config. There is no PromptDeps callback struct —
// every consumer just calls these helpers directly.

// loadEnabledSkillsFromConfig scans cfg.Skills.Paths for SKILL.md files
// and returns the enabled skills as the prompt-side Skill type.
//
// The scan runs on every call (no in-process cache) — for ~30 SKILL.md
// files across our public/custom directories, the cost is in the low
// milliseconds cold and sub-millisecond after the OS page cache warms,
// which is well below the latency floor of any LLM call. Re-introduce
// caching here only if a real workload demonstrates a problem.
//
// Errors are logged and treated as "no skills" (Python's try/except
// branch returns []).
func loadEnabledSkillsFromConfig(cfg config.Config) []Skill {
	if len(cfg.Skills.Paths) == 0 {
		return nil
	}
	loaded, err := skills.LoadFromPaths(cfg.Skills.Paths)
	if err != nil {
		slog.Warn("skills loader: scan failed", "err", err)
		return nil
	}
	out := make([]Skill, 0, len(loaded))
	for _, s := range loaded {
		if !skills.IsEnabled(s.Name, s.Category, cfg.Skills.Enabled) {
			continue
		}
		out = append(out, Skill{
			Name:        s.Name,
			Description: s.Description,
			Category:    s.Category,
			SkillFile:   s.SkillFile,
		})
	}
	return out
}

// DeferredToolNamesFromConfig returns names of tools that should be
// advertised in the prompt's <available-deferred-tools> section AND
// filtered out of the active toolbelt by the DeferredTools middleware.
// Returns nil when no deferred tools are configured.
func DeferredToolNamesFromConfig(cfg config.Config) []string {
	if len(cfg.ToolSearch.Deferred) == 0 {
		return nil
	}
	names := make([]string, 0, len(cfg.ToolSearch.Deferred))
	for _, e := range cfg.ToolSearch.Deferred {
		names = append(names, e.Name)
	}
	return names
}
