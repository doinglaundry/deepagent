package tools

import (
	"path/filepath"
	"testing"

	"eino-cli/backend/config"
	"eino-cli/backend/sandboxpaths"
)

func TestResolveToolPath_relativeUnderRepo(t *testing.T) {
	virtualPath, err := resolveToolPath("backend/foo.go", true)
	if err != nil {
		t.Fatal(err)
	}
	if virtualPath != "/mnt/repo/backend/foo.go" {
		t.Fatalf("got %q", virtualPath)
	}
}

func TestResolveToolPath_workspacePrefix(t *testing.T) {
	virtualPath, err := resolveToolPath("/mnt/workspace/note.txt", true)
	if err != nil {
		t.Fatal(err)
	}
	if virtualPath != "/mnt/workspace/note.txt" {
		t.Fatalf("got %q", virtualPath)
	}
}

func TestValidateVirtualPath_rejectsHostAbsolute(t *testing.T) {
	root := config.RootDir()
	hostPath, _ := filepath.Abs(filepath.Join(root, "main.go"))
	err := validateVirtualPath(hostPath, true)
	if err == nil {
		t.Fatal("expected error for host absolute path")
	}
}

func TestValidateVirtualPath_skillsReadOnly(t *testing.T) {
	if err := validateVirtualPath(sandboxpaths.VirtualPathPrefixSkills+"/x/SKILL.md", true); err != nil {
		t.Fatal(err)
	}
	if err := validateVirtualPath(sandboxpaths.VirtualPathPrefixSkills+"/x/SKILL.md", false); err == nil {
		t.Fatal("expected write error for skills")
	}
}
