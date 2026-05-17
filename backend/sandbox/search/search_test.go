package search

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindGlobMatchesIgnoresDir(t *testing.T) {
	tmp := t.TempDir()
	_ = os.MkdirAll(filepath.Join(tmp, "node_modules", "deep"), 0o755)
	_ = os.WriteFile(filepath.Join(tmp, "node_modules", "x.go"), []byte("//"), 0o644)
	_ = os.WriteFile(filepath.Join(tmp, "main.go"), []byte("//"), 0o644)

	matches, truncated, err := FindGlobMatches(tmp, "**/*.go", GlobOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Fatal("unexpected truncation")
	}
	if len(matches) != 1 {
		t.Fatalf("want 1 match (node_modules ignored), got %d: %v", len(matches), matches)
	}
}

func TestFindGrepMatches(t *testing.T) {
	tmp := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmp, "a.txt"), []byte("hello world\nfoo bar\n"), 0o644)
	_ = os.WriteFile(filepath.Join(tmp, "b.txt"), []byte("nothing here\n"), 0o644)

	matches, _, err := FindGrepMatches(tmp, "world", GrepOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].LineNumber != 1 {
		t.Fatalf("want 1 match on line 1, got %v", matches)
	}
}

func TestShouldIgnoreName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"foo.go", false},
		{".git", true},
		{"node_modules", true},
		{"build.log", true},
		{"main.go.bak", true},
	}
	for _, c := range cases {
		if got := ShouldIgnoreName(c.name); got != c.want {
			t.Errorf("ShouldIgnoreName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}
