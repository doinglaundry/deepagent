// Package skills implements the SKILL.md scanner used to populate the
// <available_skills> section of the lead-agent system prompt. Mirrors
// deerflow.skills.list_enabled_skills + the SKILL.md frontmatter parser.
//
// This package is a leaf — it deliberately does NOT import the agent
// package. agent.BuildPromptDeps converts skills.Skill into agent.Skill,
// keeping the import graph one-way (agent → skills, never the reverse).
package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Skill is the on-disk representation of a SKILL.md entry. Maps 1:1 to
// agent.Skill via agent.BuildPromptDeps.
type Skill struct {
	Name        string
	Description string
	Category    string // "built-in" | "custom"
	SkillFile   string
}

// Default categorization heuristic: paths under "$HOME/.cursor/skills-cursor"
// or anything ending in "/cursor/skills" are treated as built-in; everything
// else is "custom" so the agent knows it is allowed to edit / patch them.
var builtinPathFragments = []string{
	"/.cursor/skills-cursor/",
	"/cursor/skills-cursor/",
	"/.cursor/skills/",
	"/cursor/skills/",
}

// LoadFromPaths scans each search path one level deep for "<name>/SKILL.md"
// files and returns the parsed skill list. Missing paths are silently
// skipped — the prompt section degrades to "" when nothing is found, which
// matches Python's behaviour.
//
// The path argument is expanded with ~ relative to the user's $HOME.
func LoadFromPaths(paths []string) ([]Skill, error) {
	var out []Skill
	seen := map[string]struct{}{}

	for _, raw := range paths {
		resolved, err := expandPath(raw)
		if err != nil {
			continue
		}
		entries, err := os.ReadDir(resolved)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read skills dir %q: %w", resolved, err)
		}

		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			skillFile := filepath.Join(resolved, e.Name(), "SKILL.md")
			info, err := os.Stat(skillFile)
			if err != nil || info.IsDir() {
				continue
			}
			s, err := parseSkillFile(skillFile, e.Name())
			if err != nil {
				return nil, fmt.Errorf("parse skill %q: %w", skillFile, err)
			}
			if _, dup := seen[s.Name]; dup {
				continue
			}
			seen[s.Name] = struct{}{}
			out = append(out, s)
		}
	}

	return out, nil
}

// parseSkillFile extracts {name, description} from the YAML frontmatter at
// the head of a SKILL.md file. We deliberately implement a tiny line-based
// parser instead of pulling in a full YAML dependency for the leaf package,
// since the frontmatter we accept is the subset deerflow ships:
//
//	---
//	name: my-skill
//	description: One-line summary of when to use this skill.
//	---
//
// Anything that isn't recognized falls back to dirName + first-paragraph
// description, matching the deerflow "no frontmatter" path.
func parseSkillFile(path, dirName string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}

	name, description := dirName, ""
	body := string(data)

	if frontmatter, rest, ok := splitFrontmatter(body); ok {
		if v := matchFrontmatterKey(frontmatter, "name"); v != "" {
			name = v
		}
		if v := matchFrontmatterKey(frontmatter, "description"); v != "" {
			description = v
		}
		body = rest
	}
	if description == "" {
		description = firstParagraph(body)
	}

	return Skill{
		Name:        strings.TrimSpace(name),
		Description: strings.TrimSpace(description),
		Category:    classify(path),
		SkillFile:   path,
	}, nil
}

func splitFrontmatter(s string) (front, rest string, ok bool) {
	const sep = "---"
	if !strings.HasPrefix(s, sep) {
		return "", s, false
	}
	rest = strings.TrimPrefix(s, sep)
	rest = strings.TrimLeft(rest, "\r\n")
	idx := strings.Index(rest, sep)
	if idx < 0 {
		return "", s, false
	}
	front = rest[:idx]
	rest = rest[idx+len(sep):]
	rest = strings.TrimLeft(rest, "\r\n")
	return front, rest, true
}

var frontmatterKVRe = regexp.MustCompile(`(?m)^([A-Za-z0-9_-]+)\s*:\s*(.*)$`)

func matchFrontmatterKey(front, key string) string {
	for _, m := range frontmatterKVRe.FindAllStringSubmatch(front, -1) {
		if !strings.EqualFold(m[1], key) {
			continue
		}
		v := strings.TrimSpace(m[2])
		v = strings.TrimPrefix(v, `"`)
		v = strings.TrimSuffix(v, `"`)
		v = strings.TrimPrefix(v, `'`)
		v = strings.TrimSuffix(v, `'`)
		return v
	}
	return ""
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

func classify(path string) string {
	for _, frag := range builtinPathFragments {
		if strings.Contains(path, frag) {
			return "built-in"
		}
	}
	return "custom"
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
