package skills_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eino-cli/deepagent/graph/middleware"
	"eino-cli/deepagent/graph/skills"
	"eino-cli/deepagent/graph/tools"

	einotool "github.com/cloudwego/eino/components/tool"
)

func TestSkillsCatalogDefersNestedInstructionsUntilActivation(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "deepagent", "skills", "public", "test-skill")
	mkdirErr := os.MkdirAll(dir, 0700)
	if mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	body := "---\nname: test-skill\ndescription: Verify project changes.\n---\n# Testing skill\nRun the project test command before reporting completion."
	writeErr := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0600)
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	catalog, err := skills.DiscoverSkills(root, []string{"deepagent/skills"})
	if err != nil {
		t.Fatal(err)
	}
	prompt := catalogPrompt(t, catalog)
	if !strings.Contains(prompt, "test-skill") || !strings.Contains(prompt, "Verify project changes") || strings.Contains(prompt, "Run the project test command") {
		t.Fatalf("catalog eagerly includes body or lacks summary: %q", prompt)
	}
	// The bounded catalog owns the discovered content, even if files change.
	removeErr := os.Remove(filepath.Join(dir, "SKILL.md"))
	if removeErr != nil {
		t.Fatal(removeErr)
	}
	tool := tools.NewActivateSkillTool(catalog)
	if !tool.ReadOnly {
		t.Fatal("activation must be read-only")
	}
	content, err := tool.Tool.(einotool.InvokableTool).InvokableRun(context.Background(), `{"name":"test-skill"}`)
	if err != nil || !strings.Contains(content, "Run the project test command") {
		t.Fatalf("full instructions missing %q err %v", content, err)
	}
	_, err = tool.Tool.(einotool.InvokableTool).InvokableRun(context.Background(), `{"name":"../../etc/passwd"}`)
	if err == nil {
		t.Fatal("activated undiscovered skill")
	}
}

func TestSkillsRejectAmbiguousNames(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"one", "two"} {
		dir := filepath.Join(root, name)
		err := os.MkdirAll(dir, 0700)
		if err != nil {
			t.Fatal(err)
		}
		writeErr := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: same\n---\ncontent"), 0600)
		if writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	_, discoverSkillsErr := skills.DiscoverSkills(root, []string{root})
	if discoverSkillsErr == nil {
		t.Fatal("duplicate catalog name accepted")
	}
}

func TestBundledSkillCatalogFitsExampleConfiguration(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := skills.DiscoverSkills(root, []string{"deepagent/skills"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := catalog.ListSkills(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("example skill configuration found no bundled skills")
	}
	body, err := skills.LoadSkillContent(context.Background(), catalog, items[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(catalogPrompt(t, catalog), body) {
		t.Fatal("bundled body leaked into catalog")
	}
}

func catalogPrompt(t *testing.T, loader skills.SkillLoader) string {
	t.Helper()
	messages, err := middleware.NewSkillMiddleware(loader).BuildPrompt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) == 0 {
		return ""
	}
	return messages[0].Content
}
