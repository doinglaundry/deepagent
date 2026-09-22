package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromPaths_FrontmatterAndFallback(t *testing.T) {
	root := t.TempDir()

	// Skill A: full frontmatter.
	mustWrite(t, filepath.Join(root, "skill-a", "SKILL.md"),
		`---
name: skill-a
description: First-line summary of skill-a.
---

This is the body.
`)

	// Skill B: no frontmatter; description should fall back to the first
	// paragraph.
	mustWrite(t, filepath.Join(root, "skill-b", "SKILL.md"),
		`Sometimes there is no frontmatter.

Body keeps going here.
`)

	// Empty dir without SKILL.md should be ignored, not crash.
	if err := os.MkdirAll(filepath.Join(root, "skill-c"), 0o755); err != nil {
		t.Fatalf("mkdir skill-c: %v", err)
	}

	got, err := LoadFromPaths([]string{root})
	if err != nil {
		t.Fatalf("LoadFromPaths: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d skills, want 2: %+v", len(got), got)
	}

	bySlot := map[string]Skill{}
	for _, s := range got {
		bySlot[s.Name] = s
	}

	a, ok := bySlot["skill-a"]
	if !ok {
		t.Fatalf("skill-a not loaded: got %+v", got)
	}
	if a.Description != "First-line summary of skill-a." {
		t.Fatalf("skill-a description: got %q", a.Description)
	}
	if a.Category != "custom" {
		t.Fatalf("skill-a category: got %q, want custom", a.Category)
	}

	b, ok := bySlot["skill-b"]
	if !ok {
		t.Fatalf("skill-b not loaded: got %+v", got)
	}
	if b.Description != "Sometimes there is no frontmatter." {
		t.Fatalf("skill-b description: got %q", b.Description)
	}
}

func TestLoadFromPaths_DedupAcrossPaths(t *testing.T) {
	root1 := t.TempDir()
	root2 := t.TempDir()
	mustWrite(t, filepath.Join(root1, "lark-im", "SKILL.md"),
		"---\nname: lark-im\ndescription: First copy.\n---\n")
	mustWrite(t, filepath.Join(root2, "lark-im", "SKILL.md"),
		"---\nname: lark-im\ndescription: Second copy.\n---\n")

	got, err := LoadFromPaths([]string{root1, root2})
	if err != nil {
		t.Fatalf("LoadFromPaths: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected dedup to 1 skill, got %d", len(got))
	}
	if got[0].Description != "First copy." {
		t.Fatalf("dedup must keep the earlier path; got %q", got[0].Description)
	}
}

func TestLoadFromPaths_MissingPathSilentlySkipped(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "real", "SKILL.md"),
		"---\nname: real\ndescription: ok.\n---\n")

	got, err := LoadFromPaths([]string{filepath.Join(root, "does-not-exist"), root})
	if err != nil {
		t.Fatalf("LoadFromPaths: %v", err)
	}
	if len(got) != 1 || got[0].Name != "real" {
		t.Fatalf("got %+v, want one 'real' skill", got)
	}
}

func TestLoadFromPaths_PublicCustomCategoryRoot(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "public", "shared", "SKILL.md"),
		"---\nname: shared\ndescription: Built-in copy.\n---\n")
	mustWrite(t, filepath.Join(root, "public", "only-public", "SKILL.md"),
		"---\nname: only-public\ndescription: Public skill.\n---\n")
	mustWrite(t, filepath.Join(root, "custom", "shared", "SKILL.md"),
		"---\nname: shared\ndescription: Custom override.\n---\n")
	mustWrite(t, filepath.Join(root, "custom", "only-custom", "SKILL.md"),
		"---\nname: only-custom\ndescription: Custom skill.\n---\n")

	got, err := LoadFromPaths([]string{root})
	if err != nil {
		t.Fatalf("LoadFromPaths: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 skills (shared deduped), got %d: %+v", len(got), got)
	}

	by := map[string]Skill{}
	for _, s := range got {
		by[s.Name] = s
	}

	shared, ok := by["shared"]
	if !ok {
		t.Fatalf("shared not loaded")
	}
	if shared.Category != "custom" || shared.Description != "Custom override." {
		t.Fatalf("custom must override public for 'shared': %+v", shared)
	}

	pub, ok := by["only-public"]
	if !ok || pub.Category != "public" {
		t.Fatalf("only-public must be public-categorised: %+v", pub)
	}

	cus, ok := by["only-custom"]
	if !ok || cus.Category != "custom" {
		t.Fatalf("only-custom must be custom-categorised: %+v", cus)
	}
}

func TestLoadFromPaths_NewFieldsPopulated(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "skill-x", "SKILL.md"),
		`---
name: skill-x
description: Has a license field.
license: MIT
---

Body.
`)

	got, err := LoadFromPaths([]string{root})
	if err != nil {
		t.Fatalf("LoadFromPaths: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d skills, want 1", len(got))
	}
	s := got[0]
	if s.License != "MIT" {
		t.Fatalf("License: got %q, want MIT", s.License)
	}
	if s.RelativePath != "skill-x" {
		t.Fatalf("RelativePath: got %q, want skill-x", s.RelativePath)
	}
	wantDir := filepath.Join(root, "skill-x")
	if s.SkillDir != wantDir {
		t.Fatalf("SkillDir: got %q, want %q", s.SkillDir, wantDir)
	}
	if !s.Enabled {
		t.Fatalf("Enabled should default to true at load time")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
