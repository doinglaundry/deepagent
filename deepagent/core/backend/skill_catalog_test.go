package backend_test

import (
	"context"
	"eino-cli/deepagent/core/backend"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillsCatalogDefersNestedInstructionsUntilActivation(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "deepagent", "skills", "public", "test-skill")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: test-skill\ndescription: Verify project changes.\n---\n# Testing skill\nRun the project test command before reporting completion."
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := backend.DiscoverSkills(root, []string{"deepagent/skills"})
	if err != nil {
		t.Fatal(err)
	}
	prompt := catalogPrompt(t, catalog)
	if !strings.Contains(prompt, "test-skill") || !strings.Contains(prompt, "Verify project changes") || strings.Contains(prompt, "Run the project test command") {
		t.Fatalf("catalog eagerly includes body or lacks summary: %q", prompt)
	}
	// The bounded catalog owns the discovered content, even if files change.
	if err := os.Remove(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	tool := tools.NewActivateSkillTool(catalog)
	if !tool.ReadOnly() {
		t.Fatal("activation must be read-only")
	}
	content, err := tool.InvokableRun(context.Background(), `{"name":"test-skill"}`)
	if err != nil || !strings.Contains(content, "Run the project test command") {
		t.Fatalf("full instructions missing %q err %v", content, err)
	}
	if _, err = tool.InvokableRun(context.Background(), `{"name":"../../etc/passwd"}`); err == nil {
		t.Fatal("activated undiscovered skill")
	}
}
func TestSkillsRejectAmbiguousNames(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"one", "two"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: same\n---\ncontent"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := backend.DiscoverSkills(root, []string{root}); err == nil {
		t.Fatal("duplicate catalog name accepted")
	}
}

func TestBundledSkillCatalogFitsExampleConfiguration(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := backend.DiscoverSkills(root, []string{"deepagent/skills"})
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
	body, err := backend.LoadSkillContent(context.Background(), catalog, items[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(catalogPrompt(t, catalog), body) {
		t.Fatal("bundled body leaked into catalog")
	}
}

func catalogPrompt(t *testing.T, loader backend.SkillLoader) string {
	t.Helper()
	messages, err := middleware.NewSkill(loader).BuildPrompt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) == 0 {
		return ""
	}
	return messages[0].Content
}
