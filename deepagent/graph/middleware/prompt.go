package middleware

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	agentmodel "eino-cli/deepagent/model"
)

type projectInstructions struct {
	BaseMiddleware
	files agentmodel.Filesystem
}

func NewProjectInstructions(filesystem agentmodel.Filesystem) agentmodel.Middleware {
	return &projectInstructions{files: filesystem}
}

func (*projectInstructions) GetName() string { return "project_instructions" }

func (projectInstructions *projectInstructions) BuildPrompt(ctx context.Context) ([]*agentmodel.Message, error) {
	// Read the complete bounded backend file rather than the tool's default
	// 2000-line window; the requested section may occur near the end.
	lineLimit := int(^uint(0) >> 1)
	instructionsText, err := projectInstructions.files.Read(ctx, "AGENTS.md", nil, &lineLimit)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, agentmodel.ErrFileNotFound) {
			return nil, nil
		}
		return nil, err
	}
	disciplineInstructions := extractProjectInstructionSection(instructionsText, "Agent Working Discipline")
	if disciplineInstructions == "" {
		return nil, nil
	}
	return []*agentmodel.Message{agentmodel.NewSystemMessage("<agent_discipline>\n" + disciplineInstructions + "\n</agent_discipline>")}, nil
}

func extractProjectInstructionSection(instructionsText, sectionTitle string) string {
	lines := strings.Split(strings.ReplaceAll(instructionsText, "\r\n", "\n"), "\n")
	sectionStart := -1
	for i, line := range lines {
		if sectionStart < 0 {
			if strings.TrimSpace(line) == "## "+sectionTitle {
				sectionStart = i + 1
			}
			continue
		}
		if strings.HasPrefix(line, "## ") {
			return strings.TrimSpace(strings.Join(lines[sectionStart:i], "\n"))
		}
	}
	if sectionStart >= 0 {
		return strings.TrimSpace(strings.Join(lines[sectionStart:], "\n"))
	}
	return ""
}

type SkillMiddleware struct {
	BaseMiddleware
	skillLoader agentmodel.SkillLoader
}

func NewSkillMiddleware(skillLoader agentmodel.SkillLoader) agentmodel.Middleware {
	return &SkillMiddleware{skillLoader: skillLoader}
}

func (skillMiddleware *SkillMiddleware) GetName() string { return "skill" }

func (skillMiddleware *SkillMiddleware) BuildPrompt(ctx context.Context) ([]*agentmodel.Message, error) {

	if skillMiddleware == nil || skillMiddleware.skillLoader == nil {
		return nil, nil
	}

	availableSkills, err := skillMiddleware.skillLoader.ListSkills(ctx)
	if err != nil {
		return nil, err
	}
	validSkills := make([]*agentmodel.SkillMetadata, 0, len(availableSkills))
	for _, skill := range availableSkills {
		if skill != nil && strings.TrimSpace(skill.Name) != "" {
			validSkills = append(validSkills, skill)
		}
	}

	sort.Slice(validSkills, func(i, j int) bool { return validSkills[i].Name < validSkills[j].Name })

	var skillPrompt strings.Builder
	skillPrompt.WriteString("Available project skills. When a skill applies, call activate_skill with its exact name before using it. The tool returns its full instructions and source path.\n")
	for _, skill := range validSkills {
		fmt.Fprintf(&skillPrompt, "- %s: %s (source: %s)\n", skill.Name, skill.Description, skill.Path)
	}
	return []*agentmodel.Message{agentmodel.NewSystemMessage(skillPrompt.String())}, nil
}
