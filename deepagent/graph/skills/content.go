package skills

import (
	"context"
	"fmt"
	"io"
	"os"
)

// LoadSkillContent delegates to custom content loaders, or reads an exact
// catalog entry for older metadata-only loaders. File access stays in the skills package.
func LoadSkillContent(ctx context.Context, loader SkillLoader, name string) (string, error) {
	contextErr := ctx.Err()
	if contextErr != nil {
		return "", contextErr
	}
	contentLoader, ok := loader.(interface {
		LoadSkill(context.Context, string) (string, error)
	})
	if ok {
		return contentLoader.LoadSkill(ctx, name)
	}
	skillMetadata, err := loader.ListSkills(ctx)
	if err != nil {
		return "", err
	}
	for _, skill := range skillMetadata {
		if skill == nil || skill.Name != name {
			continue
		}
		file, err := os.Open(skill.Path)
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
		return fmt.Sprintf("Skill: %s\nSource: %s\n\n%s", skill.Name, skill.Path, data), nil
	}
	return "", fmt.Errorf("skill %q is unavailable", name)
}
