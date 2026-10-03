package skills

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type fileLoader []*SkillMetadata

func (fileLoader fileLoader) ListSkills(context.Context) ([]*SkillMetadata, error) {
	return append([]*SkillMetadata(nil), fileLoader...), nil
}

// LoadSkills discovers SKILL.md files. In a public/custom layout, custom skills
// replace public skills with the same name.
func LoadSkills(paths []string) (SkillLoader, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	skillMetadata := fileLoader{}
	skillIndicesByName := map[string]int{}
	for _, configuredPath := range paths {
		root, err := expandPath(configuredPath)
		if err != nil {
			return nil, err
		}
		categories := []struct {
			path    string
			replace bool
		}{{root, false}}
		if isDirectory(filepath.Join(root, "public")) || isDirectory(filepath.Join(root, "custom")) {
			categories = categories[:0]
			if isDirectory(filepath.Join(root, "public")) {
				categories = append(categories, struct {
					path    string
					replace bool
				}{filepath.Join(root, "public"), false})
			}
			if isDirectory(filepath.Join(root, "custom")) {
				categories = append(categories, struct {
					path    string
					replace bool
				}{filepath.Join(root, "custom"), true})
			}
		}
		for _, category := range categories {
			discoveredSkills, err := scanSkillDirectory(category.path)
			if err != nil {
				return nil, err
			}
			for _, skill := range discoveredSkills {
				existingIndex, exists := skillIndicesByName[skill.Name]
				if exists {
					if category.replace {
						skillMetadata[existingIndex] = skill
					}
					continue
				}
				skillIndicesByName[skill.Name] = len(skillMetadata)
				skillMetadata = append(skillMetadata, skill)
			}
		}
	}
	return skillMetadata, nil
}

func scanSkillDirectory(root string) ([]*SkillMetadata, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result []*SkillMetadata
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(root, entry.Name(), "SKILL.md")
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		name, description := entry.Name(), getFirstParagraph(string(data))
		frontmatter, body, ok := splitFrontmatter(string(data))
		if ok {
			var fields struct {
				Name, Description string
			}
			if yaml.Unmarshal([]byte(frontmatter), &fields) == nil {
				if strings.TrimSpace(fields.Name) != "" {
					name = strings.TrimSpace(fields.Name)
				}
				if strings.TrimSpace(fields.Description) != "" {
					description = strings.TrimSpace(fields.Description)
				} else {
					description = getFirstParagraph(body)
				}
			}
		}
		result = append(result, &SkillMetadata{Name: name, Description: description, Path: path})
	}
	return result, nil
}

func expandPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("empty skill path")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	return filepath.Abs(path)
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func splitFrontmatter(content string) (string, string, bool) {
	if !strings.HasPrefix(content, "---") {
		return "", content, false
	}
	remainingContent := strings.TrimLeft(strings.TrimPrefix(content, "---"), "\r\n")
	end := strings.Index(remainingContent, "\n---")
	if end < 0 {
		return "", content, false
	}
	return remainingContent[:end], strings.TrimLeft(remainingContent[end+4:], "\r\n"), true
}

func getFirstParagraph(content string) string {
	content = strings.TrimSpace(content)
	end := strings.Index(content, "\n\n")
	if end >= 0 {
		content = content[:end]
	}
	return strings.TrimSpace(content)
}
