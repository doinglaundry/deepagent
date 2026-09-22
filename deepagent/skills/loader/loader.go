// Package skills scans SKILL.md files for the <available_skills> prompt section.
// Permissive at load time; strict frontmatter enforcement lives in validation.go.
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

// Skill is one SKILL.md entry; only Name/Description/SkillFile feed the prompt.
type Skill struct {
	Name         string
	Description  string
	License      string // optional, frontmatter "license" field
	Category     string // "public" | "custom"
	SkillDir     string // absolute path to the skill's directory
	SkillFile    string // absolute path to SKILL.md
	RelativePath string // path relative to the category root
	Enabled      bool   // loader leaves true; IsEnabled overlays the extensions config
}

// LoadFromPaths scans each path for SKILL.md files (category-aware or flat).
// custom shadows public on collisions; first occurrence wins across flat paths.
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

// categoryRoot: one (path, category) entry; canOverride enables custom-shadows-public.
type categoryRoot struct {
	path        string
	category    string
	canOverride bool
}

// categoryRoots picks category-aware vs flat layout structurally (no flag).
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

// parseSkillFile is permissive: a malformed frontmatter falls back to body text.
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

// frontmatterFields: loader-relevant subset; strict whitelist is in validation.go.
type frontmatterFields struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	License     string `yaml:"license"`
}

func applyFrontmatter(skill *Skill, raw string) {
	var fm frontmatterFields
	if err := yaml.Unmarshal([]byte(raw), &fm); err != nil {
		return // tolerate malformed yaml; caller falls back to body
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

// splitFrontmatter extracts the YAML block between "---" fences; matches "\n---"
// so embedded "---" inside a multi-line string isn't truncated.
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

// IsEnabled: explicit map entry wins; otherwise public/custom default to true.
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
