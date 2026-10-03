package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSkillsCustomReplacesPublic(t *testing.T) {
	root := t.TempDir()
	for _, testCase := range []struct{ category, description string }{{"public", "public copy"}, {"custom", "custom copy"}} {
		path := filepath.Join(root, testCase.category, "review")
		err := os.MkdirAll(path, 0o700)
		if err != nil {
			t.Fatal(err)
		}
		writeErr := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte("---\nname: review\ndescription: "+testCase.description+"\n---\nbody"), 0o600)
		if writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	loader, err := LoadSkills([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	skillMetadata, err := loader.ListSkills(context.Background())
	if err != nil || len(skillMetadata) != 1 || skillMetadata[0].Description != "custom copy" {
		t.Fatalf("skills = %+v, %v", skillMetadata, err)
	}
}
