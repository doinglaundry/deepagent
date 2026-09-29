package middleware

import (
	"context"
	"eino-cli/deepagent/core/backend"
	"github.com/cloudwego/eino/components/tool"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testLoader struct{ items []*backend.SkillMetadata }

func (l testLoader) ListSkills(context.Context) ([]*backend.SkillMetadata, error) {
	return l.items, nil
}

func TestActivateSkillLoadsInstructions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(path, []byte("follow these instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	middleware := NewSkillMiddleware(testLoader{items: []*backend.SkillMetadata{{Name: "review", Description: "Review code", Path: path}}})
	prompt, err := middleware.BuildPrompt(context.Background())
	if err != nil || len(prompt) != 1 || !strings.Contains(prompt[0].Content, "review") {
		t.Fatalf("prompt = %+v, %v", prompt, err)
	}
	items, err := middleware.Tools(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("tools = %d, %v", len(items), err)
	}
	output, err := items[0].(tool.InvokableTool).InvokableRun(context.Background(), `{"name":"review"}`)
	if err != nil || !strings.Contains(output, "follow these instructions") {
		t.Fatalf("activate_skill = %q, %v", output, err)
	}
}

func TestBuildPromptIgnoresInvalidSkillMetadata(t *testing.T) {
	items := []*backend.SkillMetadata{
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
	files := mustLocalFilesystem(t, &backend.LocalFilesystemConfig{RootDir: root, VirtualMode: true})
	mw := NewProjectInstructions(files)
	if out, err := mw.BuildPrompt(context.Background()); err != nil || len(out) != 0 {
		t.Fatalf("missing file: %v %v", out, err)
	}
	cases := []struct{ text, want string }{
		{"## Other\nstyle only", ""},
		{"## Agent Working Discipline extra\nwrong section", ""},
		{"## Other\nstyle only\n## Agent Working Discipline\nfollow project\n### Nested\nretain\n## Other\nomit", "follow project\n### Nested\nretain"},
		{strings.Repeat("line\n", 2100) + "## Agent Working Discipline\nlate section", "late section"},
		{"## Agent Working Discipline\r\nwindows\r\n## Other\r\nomit", "windows"},
	}
	for _, tc := range cases {
		if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(tc.text), 0600); err != nil {
			t.Fatal(err)
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
	if _, err := mw.BuildPrompt(ctx); err != context.Canceled {
		t.Fatalf("cancel=%v", err)
	}
}
