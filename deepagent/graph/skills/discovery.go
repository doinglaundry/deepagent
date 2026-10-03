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
	skillCatalog := &skillCatalog{skills: map[string]projectSkill{}}
	paths := append([]string(nil), configured...)
	if len(paths) == 0 {
		paths = []string{filepath.Join(workDir, ".agents", "skills"), filepath.Join(workDir, ".codex", "skills")}
	}
	seenFiles := map[string]bool{}
	seenDirectories := map[string]bool{}
	totalBytes := 0
	var loadSkillPath func(string, bool) error
	loadSkillPath = func(path string, required bool) error {
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
		canonicalPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		if info.IsDir() {
			if seenDirectories[canonicalPath] {
				return nil
			}
			seenDirectories[canonicalPath] = true
			if len(seenDirectories) > 1024 {
				return fmt.Errorf("skill directory scan exceeds 1024 directories")
			}
			skillPath := filepath.Join(path, "SKILL.md")
			_, statErr := os.Stat(skillPath)
			if statErr == nil {
				return loadSkillPath(skillPath, true)
			}
			entries, err := os.ReadDir(path)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if entry.IsDir() {
					err := loadSkillPath(filepath.Join(path, entry.Name()), false)
					if err != nil {
						return err
					}
				}
			}
			return nil
		}
		if seenFiles[canonicalPath] {
			return nil
		}
		seenFiles[canonicalPath] = true
		if len(seenFiles) > 64 || info.Size() > 128<<10 {
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
		_, exists := skillCatalog.skills[skill.name]
		if exists {
			return fmt.Errorf("duplicate skill name %q; configure an unambiguous collection", skill.name)
		}
		skillCatalog.skills[skill.name] = skill
		skillCatalog.names = append(skillCatalog.names, skill.name)
		return nil
	}
	for _, path := range paths {
		err := loadSkillPath(path, len(configured) > 0)
		if err != nil {
			return nil, fmt.Errorf("load skill catalog: %w", err)
		}
	}
	sort.Strings(skillCatalog.names)
	return skillCatalog, nil
}
func (skillCatalog *skillCatalog) ListSkills(ctx context.Context) ([]*SkillMetadata, error) {
	err := ctx.Err()
	if err != nil {
		return nil, err
	}
	skillMetadata := make([]*SkillMetadata, 0, len(skillCatalog.names))
	for _, name := range skillCatalog.names {
		skill := skillCatalog.skills[name]
		skillMetadata = append(skillMetadata, &SkillMetadata{Name: skill.name, Description: skill.description, Path: skill.path})
	}
	return skillMetadata, nil
}
func (skillCatalog *skillCatalog) LoadSkill(ctx context.Context, name string) (string, error) {
	err := ctx.Err()
	if err != nil {
		return "", err
	}
	skill, ok := skillCatalog.skills[name]
	if !ok {
		return "", fmt.Errorf("skill %q is not in the configured catalog", name)
	}
	return fmt.Sprintf("Skill: %s\nSource: %s\n\n%s", skill.name, skill.path, skill.body), nil
}
