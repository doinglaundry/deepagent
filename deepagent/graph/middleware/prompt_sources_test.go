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

func (l testLoader) ListSkills(context.Context) ([]*skillspkg.SkillMetadata, error) {
	return l.items, nil
}

func TestBuildPromptIgnoresInvalidSkillMetadata(t *testing.T) {
	items := []*skillspkg.SkillMetadata{
		nil,
		{Name: " ", Description: "invalid skill"},
		{Name: "review", Description: "Review code", Path: "/skills/review/SKILL.md"},
		{Name: "build", Description: "Build code", Path: "/skills/build/SKILL.md"},
	}
	middleware := NewSkillMiddleware(testLoader{items: items})
	prompt, err := middleware.BuildPrompt(context.Background())
	if err != nil || len(prompt) != 1 || !strings.Contains(prompt[0].Content, "review") {
		t.Fatalf("prompt = %+v, %v", prompt, err)
	}
	text := prompt[0].Content
	if strings.Contains(text, "invalid skill") || strings.Index(text, "- build:") < 0 || strings.Index(text, "- build:") > strings.Index(text, "- review:") {
		t.Fatalf("unexpected skill list: %s", text)
	}
	if items[0] != nil || items[1].Name != " " || items[2].Name != "review" || items[3].Name != "build" {
		t.Fatalf("loader-owned slice was modified: %+v", items)
	}
}

func TestProjectInstructionsOnlyLoadDiscipline(t *testing.T) {
	root := t.TempDir()
	files := mustLocalFilesystem(t, &filesystempkg.LocalFilesystemConfig{RootDir: root, VirtualMode: true})
	mw := NewProjectInstructions(files)
	buildPromptOut, mwBuildPromptErr := mw.BuildPrompt(context.Background())
	if mwBuildPromptErr != nil || len(buildPromptOut) != 0 {
		t.Fatalf("missing file: %v %v", buildPromptOut, mwBuildPromptErr)
	}
	cases := []struct{ text, want string }{
		{"## Other\nstyle only", ""},
		{"## Agent Working Discipline extra\nwrong section", ""},
		{"## Other\nstyle only\n## Agent Working Discipline\nfollow project\n### Nested\nretain\n## Other\nomit", "follow project\n### Nested\nretain"},
		{strings.Repeat("line\n", 2100) + "## Agent Working Discipline\nlate section", "late section"},
		{"## Agent Working Discipline\r\nwindows\r\n## Other\r\nomit", "windows"},
	}
	for _, tc := range cases {
		writeErr := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(tc.text), 0600)
		if writeErr != nil {
			t.Fatal(writeErr)
		}
		out, err := mw.BuildPrompt(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if tc.want == "" {
			if len(out) != 0 {
				t.Fatal(out)
			}
			continue
		}
		if len(out) != 1 || out[0].Content != "<agent_discipline>\n"+tc.want+"\n</agent_discipline>" {
			t.Fatalf("output=%v want=%q", out, tc.want)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, buildPromptErr := mw.BuildPrompt(ctx)
	if buildPromptErr != context.Canceled {
		t.Fatalf("cancel=%v", buildPromptErr)
	}
}
