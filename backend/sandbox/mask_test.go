package sandbox

import (
	"path/filepath"
	"strings"
	"testing"

	"eino-cli/backend/sandboxpaths"
)

func TestMaskHostPathsInOutput(t *testing.T) {
	tmp := t.TempDir()
	abs, _ := filepath.Abs(tmp)
	mappings := []sandboxpaths.MountMapping{
		{VirtualPath: sandboxpaths.VirtualPathPrefixRepo, HostPath: abs},
	}
	out := "see " + filepath.Join(abs, "main.go")
	masked := MaskHostPathsInOutput(mappings, out)
	if !strings.Contains(masked, sandboxpaths.VirtualPathPrefixRepo+"/main.go") {
		t.Fatalf("got %q", masked)
	}
}
