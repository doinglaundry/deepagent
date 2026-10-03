package middleware

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	filesystempkg "eino-cli/deepagent/graph/filesystem"
	skillspkg "eino-cli/deepagent/graph/skills"
)

type testLoader struct{ items []*skillspkg.SkillMetadata }

func (skillLoader testLoader) ListSkills(context.Context) ([]*skillspkg.SkillMetadata, error) {
	return skillLoader.items, nil
}

func TestBuildPromptIgnoresInvalidSkillMetadata(t *testing.T) {
	availableSkills := []*skillspkg.SkillMetadata{
		nil,
		{Name: " ", Description: "invalid skill"},
		{Name: "review", Description: "Review code", Path: "/skills/review/SKILL.md"},
		{Name: "build", Description: "Build code", Path: "/skills/build/SKILL.md"},
	}
	skillMiddleware := NewSkillMiddleware(testLoader{items: availableSkills})
	promptMessages, err := skillMiddleware.BuildPrompt(context.Background())
	if err != nil || len(promptMessages) != 1 || !strings.Contains(promptMessages[0].Content, "review") {
		t.Fatalf("prompt = %+v, %v", promptMessages, err)
	}
	promptText := promptMessages[0].Content
	if strings.Contains(promptText, "invalid skill") || strings.Index(promptText, "- build:") < 0 || strings.Index(promptText, "- build:") > strings.Index(promptText, "- review:") {
		t.Fatalf("unexpected skill list: %s", promptText)
	}
	if availableSkills[0] != nil || availableSkills[1].Name != " " || availableSkills[2].Name != "review" || availableSkills[3].Name != "build" {
		t.Fatalf("loader-owned slice was modified: %+v", availableSkills)
	}
}

func TestProjectInstructionsOnlyLoadDiscipline(t *testing.T) {
	workspaceRoot := t.TempDir()
	filesystem := newTestLocalFilesystem(t, &filesystempkg.LocalFilesystemConfig{RootDir: workspaceRoot, VirtualMode: true})
	projectInstructions := NewProjectInstructions(filesystem)
	missingFileMessages, missingFileErr := projectInstructions.BuildPrompt(context.Background())
	if missingFileErr != nil || len(missingFileMessages) != 0 {
		t.Fatalf("missing file: %v %v", missingFileMessages, missingFileErr)
	}
	testCases := []struct{ text, want string }{
		{"## Other\nstyle only", ""},
		{"## Agent Working Discipline extra\nwrong section", ""},
		{"## Other\nstyle only\n## Agent Working Discipline\nfollow project\n### Nested\nretain\n## Other\nomit", "follow project\n### Nested\nretain"},
		{strings.Repeat("line\n", 2100) + "## Agent Working Discipline\nlate section", "late section"},
		{"## Agent Working Discipline\r\nwindows\r\n## Other\r\nomit", "windows"},
	}
	for _, testCase := range testCases {
		writeErr := os.WriteFile(filepath.Join(workspaceRoot, "AGENTS.md"), []byte(testCase.text), 0600)
		if writeErr != nil {
			t.Fatal(writeErr)
		}
		promptMessages, err := projectInstructions.BuildPrompt(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if testCase.want == "" {
			if len(promptMessages) != 0 {
				t.Fatal(promptMessages)
			}
			continue
		}
		if len(promptMessages) != 1 || promptMessages[0].Content != "<agent_discipline>\n"+testCase.want+"\n</agent_discipline>" {
			t.Fatalf("output=%v want=%q", promptMessages, testCase.want)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, canceledReadErr := projectInstructions.BuildPrompt(ctx)
	if canceledReadErr != context.Canceled {
		t.Fatalf("cancel=%v", canceledReadErr)
	}
}
