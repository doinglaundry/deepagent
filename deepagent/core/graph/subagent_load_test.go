package graph

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func writeSpec(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SUBAGENT.yaml"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestLoadSubAgentsConfigurationAndOrdering(t *testing.T) {
	root := t.TempDir()
	writeSpec(t, root, "z", "name: reviewer\nsystem_prompt: review carefully\nmax_steps: 12\nread_only: true\nenable_filesystem: true\ntools: [read_file]\n")
	writeSpec(t, root, "a", "system_prompt: analyze\n")
	agents, err := loadSubAgentsFromDir(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 || agents[0].Name != "a" || agents[1].Name != "reviewer" {
		t.Fatalf("agents=%+v", agents)
	}
	a := agents[1]
	if a.SystemPrompt != "review carefully" || a.MaxSteps != 12 || !a.ReadOnly || !a.EnableFilesystem || a.EnableWeb {
		t.Fatalf("config=%+v", a)
	}
	if !a.ToolMask(context.Background(), &schema.ToolInfo{Name: "read_file"}) || a.ToolMask(context.Background(), &schema.ToolInfo{Name: "write_file"}) {
		t.Fatal("tool allowlist not applied")
	}
}
func TestLoadSubAgentsRejectsInvalidAndEscapingSpecs(t *testing.T) {
	for _, content := range []string{"system_prompt: x\nunknown: true\n", "system_prompt: x\nmax_steps: -1\n", "name: empty\n", "system_prompt: x\n---\nname: second\n", "system_prompt: x\ntools: [read_file, read_file]\n"} {
		root := t.TempDir()
		writeSpec(t, root, "bad", content)
		if _, err := loadSubAgentsFromDir(context.Background(), root); err == nil {
			t.Fatalf("accepted %q", content)
		}
	}
	root, outside := t.TempDir(), t.TempDir()
	writeSpec(t, outside, "external", "system_prompt: outside\n")
	if err := os.Mkdir(filepath.Join(root, "escape"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "external", "SUBAGENT.yaml"), filepath.Join(root, "escape", "SUBAGENT.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSubAgentsFromDir(context.Background(), root); err == nil {
		t.Fatal("followed escaping config symlink")
	}
}
