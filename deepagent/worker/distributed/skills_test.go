package distributed

import (
	"context"
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
	catalog, err := discoverSkills(root, []string{"deepagent/skills"})
	if err != nil {
		t.Fatal(err)
	}
	prompt := catalog.prompt()
	if !strings.Contains(prompt, "test-skill") || !strings.Contains(prompt, "Verify project changes") || strings.Contains(prompt, "Run the project test command") {
		t.Fatalf("catalog eagerly includes body or lacks summary: %q", prompt)
	}
	tool := catalog.tool()
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
	if _, err := discoverSkills(root, []string{root}); err == nil {
		t.Fatal("duplicate catalog name accepted")
	}
}

func TestBundledSkillCatalogFitsExampleConfiguration(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := discoverSkills(root, []string{"deepagent/skills"})
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.names) == 0 {
		t.Fatal("example skill configuration found no bundled skills")
	}
	if strings.Contains(catalog.prompt(), catalog.skills[catalog.names[0]].body) {
		t.Fatal("bundled body leaked into catalog")
	}
}
