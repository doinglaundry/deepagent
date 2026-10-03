package skills

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type projectSkill struct{ name, description, path, body string }
type skillCatalog struct {
	skills map[string]projectSkill
	names  []string
}

func DiscoverSkills(workDir string, configured []string) (SkillLoader, error) {
	catalog := &skillCatalog{skills: map[string]projectSkill{}}
	paths := append([]string(nil), configured...)
	if len(paths) == 0 {
		paths = []string{filepath.Join(workDir, ".agents", "skills"), filepath.Join(workDir, ".codex", "skills")}
	}
	seen := map[string]bool{}
	directories := map[string]bool{}
	totalBytes := 0
	var load func(string, bool) error
	load = func(path string, required bool) error {
		if !filepath.IsAbs(path) {
			path = filepath.Join(workDir, path)
		}
		info, err := os.Stat(path)
		if os.IsNotExist(err) && !required {
			return nil
		}
		if err != nil {
			return err
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		if info.IsDir() {
			if directories[canonical] {
				return nil
			}
			directories[canonical] = true
			if len(directories) > 1024 {
				return fmt.Errorf("skill directory scan exceeds 1024 directories")
			}
			direct := filepath.Join(path, "SKILL.md")
			_, statErr := os.Stat(direct)
			if statErr == nil {
				return load(direct, true)
			}
			entries, err := os.ReadDir(path)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if entry.IsDir() {
					err := load(filepath.Join(path, entry.Name()), false)
					if err != nil {
						return err
					}
				}
			}
			return nil
		}
		if seen[canonical] {
			return nil
		}
		seen[canonical] = true
		if len(seen) > 64 || info.Size() > 128<<10 {
			return fmt.Errorf("configured skills exceed 64 files or 128 KiB per skill")
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(file, (128<<10)+1))
		_ = file.Close()
		if err != nil {
			return err
		}
		totalBytes += len(data)
		if len(data) > 128<<10 || totalBytes > 512<<10 {
			return fmt.Errorf("configured skills exceed size limits")
		}
		skill := projectSkill{name: filepath.Base(filepath.Dir(path)), path: path, body: string(data)}
		if strings.HasPrefix(skill.body, "---\n") || strings.HasPrefix(skill.body, "---\r\n") {
			normalized := strings.ReplaceAll(skill.body, "\r\n", "\n")
			remaining := strings.TrimPrefix(normalized, "---\n")
			end := strings.Index(remaining, "\n---")
			if end >= 0 {
				var metadata struct {
					Name        string `yaml:"name"`
					Description string `yaml:"description"`
				}
				err := yaml.Unmarshal([]byte(remaining[:end]), &metadata)
				if err != nil {
					return fmt.Errorf("skill metadata %s: %w", path, err)
				}
				if strings.TrimSpace(metadata.Name) != "" {
					skill.name = strings.TrimSpace(metadata.Name)
				}
				skill.description = strings.Join(strings.Fields(metadata.Description), " ")
			}
		}
		if skill.description == "" {
			skill.description = "Project skill instructions."
		}
		if len(skill.description) > 1000 {
			skill.description = skill.description[:1000] + "…"
		}
		_, exists := catalog.skills[skill.name]
		if exists {
			return fmt.Errorf("duplicate skill name %q; configure an unambiguous collection", skill.name)
		}
		catalog.skills[skill.name] = skill
		catalog.names = append(catalog.names, skill.name)
		return nil
	}
	for _, path := range paths {
		err := load(path, len(configured) > 0)
		if err != nil {
			return nil, fmt.Errorf("load skill catalog: %w", err)
		}
	}
	sort.Strings(catalog.names)
	return catalog, nil
}
func (c *skillCatalog) ListSkills(ctx context.Context) ([]*SkillMetadata, error) {
	err := ctx.Err()
	if err != nil {
		return nil, err
	}
	items := make([]*SkillMetadata, 0, len(c.names))
	for _, name := range c.names {
		s := c.skills[name]
		items = append(items, &SkillMetadata{Name: s.name, Description: s.description, Path: s.path})
	}
	return items, nil
}
func (c *skillCatalog) LoadSkill(ctx context.Context, name string) (string, error) {
	err := ctx.Err()
	if err != nil {
		return "", err
	}
	skill, ok := c.skills[name]
	if !ok {
		return "", fmt.Errorf("skill %q is not in the configured catalog", name)
	}
	return fmt.Sprintf("Skill: %s\nSource: %s\n\n%s", skill.name, skill.path, skill.body), nil
}
