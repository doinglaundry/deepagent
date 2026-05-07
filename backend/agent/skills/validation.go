package skills

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// AllowedFrontmatterFields mirrors deerflow.skills.validation.
// ALLOWED_FRONTMATTER_PROPERTIES — the closed set of keys a SKILL.md
// frontmatter may contain. Anything outside this set fails strict
// validation. The loader's parser (loader.go) is intentionally
// permissive and ignores unknown keys; this whitelist only fires on
// the install/edit paths so a hand-written custom skill can't smuggle
// arbitrary fields past review.
var AllowedFrontmatterFields = map[string]struct{}{
	"name":          {},
	"description":   {},
	"license":       {},
	"allowed-tools": {},
	"metadata":      {},
	"compatibility": {},
	"version":       {},
	"author":        {},
}

// nameRe matches deerflow's hyphen-case rule:
// lowercase letters, digits, hyphens. Empty strings, leading/trailing
// hyphens, and "--" runs are rejected by additional checks below to
// keep the regex small.
var nameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

const (
	maxNameLen        = 64
	maxDescriptionLen = 1024
)

// ValidateFrontmatter is the strict checker invoked before a skill is
// created or edited. It returns the canonical (trimmed) name on
// success and a non-empty reason on failure. Mirrors deerflow's
// _validate_skill_frontmatter contract:
//
//   - frontmatter must open with "---" and parse as a YAML mapping;
//   - only AllowedFrontmatterFields keys are accepted;
//   - name + description are required; name must be hyphen-case and
//     ≤64 chars; description must be ≤1024 chars and must not contain
//     angle brackets (which would clash with the prompt's <skill> XML
//     wrappers).
//
// We deliberately don't try to surface every possible error in one
// pass — first failure short-circuits, mirroring the Python impl and
// keeping reason strings readable to users.
func ValidateFrontmatter(content []byte) (name string, reason string) {
	body := string(content)
	if !strings.HasPrefix(body, "---") {
		return "", "no YAML frontmatter found"
	}
	front, _, ok := splitFrontmatter(body)
	if !ok {
		return "", "invalid frontmatter format"
	}

	var raw map[string]any
	if err := yaml.Unmarshal([]byte(front), &raw); err != nil {
		return "", fmt.Sprintf("invalid YAML in frontmatter: %v", err)
	}
	if raw == nil {
		return "", "frontmatter must be a YAML mapping"
	}
	for k := range raw {
		if _, ok := AllowedFrontmatterFields[k]; !ok {
			return "", fmt.Sprintf("unexpected key %q in frontmatter", k)
		}
	}

	nameAny, hasName := raw["name"]
	if !hasName {
		return "", "missing 'name' in frontmatter"
	}
	descAny, hasDesc := raw["description"]
	if !hasDesc {
		return "", "missing 'description' in frontmatter"
	}

	if nameAny == nil {
		return "", "name cannot be empty"
	}
	canonical, ok := nameAny.(string)
	if !ok {
		return "", fmt.Sprintf("name must be a string, got %T", nameAny)
	}
	canonical = strings.TrimSpace(canonical)
	if canonical == "" {
		return "", "name cannot be empty"
	}
	if !nameRe.MatchString(canonical) {
		return "", fmt.Sprintf("name %q must be hyphen-case (lowercase letters, digits, hyphens)", canonical)
	}
	if strings.HasPrefix(canonical, "-") || strings.HasSuffix(canonical, "-") || strings.Contains(canonical, "--") {
		return "", fmt.Sprintf("name %q cannot start/end with hyphen or contain consecutive hyphens", canonical)
	}
	if len(canonical) > maxNameLen {
		return "", fmt.Sprintf("name is too long (%d chars), max %d", len(canonical), maxNameLen)
	}

	desc, ok := descAny.(string)
	if !ok {
		return "", fmt.Sprintf("description must be a string, got %T", descAny)
	}
	desc = strings.TrimSpace(desc)
	if strings.ContainsAny(desc, "<>") {
		return "", "description cannot contain angle brackets"
	}
	if len(desc) > maxDescriptionLen {
		return "", fmt.Sprintf("description is too long (%d chars), max %d", len(desc), maxDescriptionLen)
	}

	return canonical, ""
}
