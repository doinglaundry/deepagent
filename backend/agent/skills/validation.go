package skills

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// AllowedFrontmatterFields: closed key set enforced on install/edit only.
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

// nameRe: hyphen-case (lowercase + digits + "-"); edge cases checked separately.
var nameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

const (
	maxNameLen        = 64
	maxDescriptionLen = 1024
)

// ValidateFrontmatter strictly checks an install/edit SKILL.md; returns the
// canonical name on success or a human-readable reason on failure (first error wins).
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
