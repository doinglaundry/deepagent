package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type fileLoader []*SkillMetadata

func (l fileLoader) ListSkills(context.Context) ([]*SkillMetadata, error) {
	return append([]*SkillMetadata(nil), l...), nil
}

// Load discovers SKILL.md files. In a public/custom layout, custom skills
// replace public skills with the same name.
func LoadSkills(paths []string) (SkillLoader, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	items := fileLoader{}
	byName := map[string]int{}
	for _, raw := range paths {
		root, err := expandPath(raw)
		if err != nil {
			return nil, err
		}
		categories := []struct {
			path    string
			replace bool
		}{{root, false}}
		if directory(filepath.Join(root, "public")) || directory(filepath.Join(root, "custom")) {
			categories = categories[:0]
			if directory(filepath.Join(root, "public")) {
				categories = append(categories, struct {
					path    string
					replace bool
				}{filepath.Join(root, "public"), false})
			}
			if directory(filepath.Join(root, "custom")) {
				categories = append(categories, struct {
					path    string
					replace bool
				}{filepath.Join(root, "custom"), true})
			}
		}
		for _, category := range categories {
			found, err := scan(category.path)
			if err != nil {
				return nil, err
			}
			for _, item := range found {
				if i, exists := byName[item.Name]; exists {
					if category.replace {
						items[i] = item
					}
					continue
				}
				byName[item.Name] = len(items)
				items = append(items, item)
			}
		}
	}
	return items, nil
}

func scan(root string) ([]*SkillMetadata, error) {
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
		name, description := entry.Name(), firstParagraph(string(data))
		if front, body, ok := splitFrontmatter(string(data)); ok {
			var fields struct {
				Name, Description string
			}
			if yaml.Unmarshal([]byte(front), &fields) == nil {
				if strings.TrimSpace(fields.Name) != "" {
					name = strings.TrimSpace(fields.Name)
				}
				if strings.TrimSpace(fields.Description) != "" {
					description = strings.TrimSpace(fields.Description)
				} else {
					description = firstParagraph(body)
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

func directory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func splitFrontmatter(content string) (string, string, bool) {
	if !strings.HasPrefix(content, "---") {
		return "", content, false
	}
	rest := strings.TrimLeft(strings.TrimPrefix(content, "---"), "\r\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", content, false
	}
	return rest[:end], strings.TrimLeft(rest[end+4:], "\r\n"), true
}

func firstParagraph(content string) string {
	content = strings.TrimSpace(content)
	if end := strings.Index(content, "\n\n"); end >= 0 {
		content = content[:end]
	}
	return strings.TrimSpace(content)
}

type SkillLoader interface {
	ListSkills(context.Context) ([]*SkillMetadata, error)
}
type SkillMetadata struct {
	Name        string
	Description string
	Path        string
}

// LoadSkillContent delegates to custom content loaders, or reads an exact
// catalog entry for older metadata-only loaders. File access stays in backend.
func LoadSkillContent(ctx context.Context, loader SkillLoader, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if custom, ok := loader.(interface {
		LoadSkill(context.Context, string) (string, error)
	}); ok {
		return custom.LoadSkill(ctx, name)
	}
	items, err := loader.ListSkills(ctx)
	if err != nil {
		return "", err
	}
	for _, item := range items {
		if item == nil || item.Name != name {
			continue
		}
		file, err := os.Open(item.Path)
		if err != nil {
			return "", err
		}
		data, err := io.ReadAll(io.LimitReader(file, (128<<10)+1))
		_ = file.Close()
		if err != nil {
			return "", err
		}
		if len(data) > 128<<10 {
			return "", fmt.Errorf("skill %q exceeds 128 KiB", name)
		}
		return fmt.Sprintf("Skill: %s\nSource: %s\n\n%s", item.Name, item.Path, data), nil
	}
	return "", fmt.Errorf("skill %q is unavailable", name)
}
