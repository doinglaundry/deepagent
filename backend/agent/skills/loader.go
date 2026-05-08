// Package skills implements the SKILL.md scanner used to populate the
// <available_skills> section of the lead-agent system prompt. Mirrors
// deerflow.skills.load_skills + the SKILL.md frontmatter parser:
//
//   - "public" + "custom" two-category layout, with custom overriding
//     public on name collisions (deerflow's "later wins" semantics);
//   - permissive parser at load time (loader.go) — strict frontmatter
//     enforcement lives in validation.go for install/edit flows;
//   - frontmatter parsed via gopkg.in/yaml.v3 so list/map fields
//     (allowed-tools, metadata, ...) round-trip cleanly even though
//     the loader only consumes name/description/license.
//
// This package is a leaf — it deliberately does NOT import the agent
// package. agent.loadEnabledSkillsFromConfig converts skills.Skill into
// agent.Skill, keeping the import graph one-way (agent → skills, never
// the reverse).
package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Skill is the on-disk representation of a SKILL.md entry. Mirrors
// deerflow.skills.types.Skill: only name/description/SkillFile feed
// the prompt (progressive-loading pattern — the agent uses read_file
// to pull SKILL.md / references / templates / scripts on demand), the
// rest is metadata for callers that manage skill lifecycle.
type Skill struct {
	Name         string
	Description  string
	License      string // optional, frontmatter "license" field
	Category     string // "public" | "custom"
	SkillDir     string // absolute path to the skill's directory
	SkillFile    string // absolute path to SKILL.md
	RelativePath string // path relative to the category root
	Enabled      bool   // populated by IsEnabled callers via the extensions config; loader leaves it true so a no-config caller still sees every skill
}

// LoadFromPaths scans each search path for SKILL.md files and returns
// the parsed list. Each path is interpreted in one of two ways:
//
//   - Category root: contains a "public/" and/or "custom/" subdir.
//     Skills under each subdir inherit that category. Custom skills
//     override same-named public skills (deerflow semantics).
//   - Flat root: contains "<name>/SKILL.md" directly. Every skill
//     is bucketed as "custom" so the agent treats it as editable.
//
// Missing paths are silently skipped — the prompt section degrades
// to "" when nothing is found, mirroring deerflow. When the same
// name appears across multiple input paths in flat mode, the first
// occurrence wins (matches the historical TestLoadFromPaths_DedupAcrossPaths
// contract).
func LoadFromPaths(paths []string) ([]Skill, error) {
	out := []Skill{}
	idxByName := map[string]int{}

	for _, raw := range paths {
		resolved, err := expandPath(raw)
		if err != nil || resolved == "" {
			continue
		}

		for _, root := range categoryRoots(resolved) {
			loaded, err := scanCategoryRoot(root.path, root.category)
			if err != nil {
				return nil, err
			}
			for _, s := range loaded {
				if idx, dup := idxByName[s.Name]; dup {
					if root.canOverride {
						out[idx] = s
					}
					continue
				}
				idxByName[s.Name] = len(out)
				out = append(out, s)
			}
		}
	}

	return out, nil
}

// categoryRoot is one (path, category) entry produced by interpreting
// a user-supplied path. canOverride is true for the "custom" leg of a
// category-aware root so deerflow's "custom shadows public" semantics
// holds.
type categoryRoot struct {
	path        string
	category    string
	canOverride bool
}

// categoryRoots decides whether the given path is a deerflow-style
// category-aware root or a plain flat root. The decision is purely
// structural — no flag, no config — so a single skills entry in
// config.yaml can point at either layout.
func categoryRoots(resolved string) []categoryRoot {
	publicRoot := filepath.Join(resolved, "public")
	customRoot := filepath.Join(resolved, "custom")
	publicExists := dirExists(publicRoot)
	customExists := dirExists(customRoot)

	if publicExists || customExists {
		var out []categoryRoot
		if publicExists {
			out = append(out, categoryRoot{path: publicRoot, category: "public"})
		}
		if customExists {
			out = append(out, categoryRoot{path: customRoot, category: "custom", canOverride: true})
		}
		return out
	}

	return []categoryRoot{{path: resolved, category: "custom"}}
}

func scanCategoryRoot(root, category string) ([]Skill, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read skills dir %q: %w", root, err)
	}

	var out []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		skillFile := filepath.Join(root, e.Name(), "SKILL.md")
		info, err := os.Stat(skillFile)
		if err != nil || info.IsDir() {
			continue
		}
		s, err := parseSkillFile(skillFile, e.Name(), category)
		if err != nil {
			return nil, fmt.Errorf("parse skill %q: %w", skillFile, err)
		}
		out = append(out, s)
	}
	return out, nil
}

// parseSkillFile is the permissive load-time parser. It tolerates
// frontmatter that yaml.v3 considers malformed (falls back to the
// body for a description) so a single bad SKILL.md doesn't blow up
// the whole prompt section. Strict validation is the install/edit
// path's job (validation.go).
func parseSkillFile(path, dirName, category string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}

	skill := Skill{
		Name:         dirName,
		Category:     category,
		SkillDir:     filepath.Dir(path),
		SkillFile:    path,
		RelativePath: dirName,
		Enabled:      true,
	}

	body := string(data)
	if frontmatter, rest, ok := splitFrontmatter(body); ok {
		applyFrontmatter(&skill, frontmatter)
		body = rest
	}
	if skill.Description == "" {
		skill.Description = firstParagraph(body)
	}
	skill.Name = strings.TrimSpace(skill.Name)
	skill.Description = strings.TrimSpace(skill.Description)
	return skill, nil
}

// frontmatterFields is the subset the loader cares about. Other
// allowed keys (allowed-tools, metadata, compatibility, version,
// author) are accepted by yaml.v3 silently — the strict whitelist
// lives in validation.AllowedFrontmatterFields.
type frontmatterFields struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	License     string `yaml:"license"`
}

func applyFrontmatter(skill *Skill, raw string) {
	var fm frontmatterFields
	// Tolerate malformed YAML at load time — caller falls back to
	// the body for a description. Strict validation is elsewhere.
	if err := yaml.Unmarshal([]byte(raw), &fm); err != nil {
		return
	}
	if v := strings.TrimSpace(fm.Name); v != "" {
		skill.Name = v
	}
	if v := strings.TrimSpace(fm.Description); v != "" {
		skill.Description = v
	}
	if v := strings.TrimSpace(fm.License); v != "" {
		skill.License = v
	}
}

// splitFrontmatter extracts the YAML block between leading and
// trailing "---" fences. Looks for "\n---" rather than a bare "---"
// so frontmatter content containing "---" (e.g. inside a multi-line
// string) doesn't get truncated.
func splitFrontmatter(s string) (front, rest string, ok bool) {
	const sep = "---"
	if !strings.HasPrefix(s, sep) {
		return "", s, false
	}
	rest = strings.TrimPrefix(s, sep)
	rest = strings.TrimLeft(rest, "\r\n")
	idx := strings.Index(rest, "\n"+sep)
	if idx < 0 {
		return "", s, false
	}
	front = rest[:idx]
	rest = rest[idx+len("\n"+sep):]
	rest = strings.TrimLeft(rest, "\r\n")
	return front, rest, true
}

func firstParagraph(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	if idx := strings.Index(body, "\n\n"); idx >= 0 {
		return strings.TrimSpace(body[:idx])
	}
	return strings.TrimSpace(body)
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// IsEnabled mirrors deerflow's ExtensionsConfig.is_skill_enabled:
// an explicit map entry wins; otherwise public/custom skills default
// to enabled. Category exists as a parameter so future extensions
// (e.g. a "core" category that ignores the map) can plug in without
// signature churn — current behaviour treats public and custom
// identically.
func IsEnabled(name, category string, enabled map[string]bool) bool {
	if v, ok := enabled[name]; ok {
		return v
	}
	return category == "public" || category == "custom"
}

func expandPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty path")
	}
	if strings.HasPrefix(raw, "~/") || raw == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		raw = filepath.Join(home, strings.TrimPrefix(raw, "~"))
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return raw, nil
	}
	return abs, nil
}
