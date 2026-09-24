package backend

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSkillsCustomReplacesPublic(t *testing.T) {
	root := t.TempDir()
	for _, item := range []struct{ category, description string }{{"public", "public copy"}, {"custom", "custom copy"}} {
		path := filepath.Join(root, item.category, "review")
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte("---\nname: review\ndescription: "+item.description+"\n---\nbody"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	loader, err := LoadSkills([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	items, err := loader.ListSkills(context.Background())
	if err != nil || len(items) != 1 || items[0].Description != "custom copy" {
		t.Fatalf("skills = %+v, %v", items, err)
	}
}
