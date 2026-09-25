package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eino-cli/deepagent/core/backend"
)

func TestGrepPreservesLiteralQueryAlias(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "data.txt"), []byte("a.b\naxb\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workspace := backend.NewFilesystemBackend(&backend.FilesystemBackendConfig{RootDir: root, VirtualMode: true})
	search := &fileSearchTool{backend: workspace, name: "grep"}
	result, err := search.InvokableRun(context.Background(), `{"query":"a.b","path":"."}`)
	if err != nil || !strings.Contains(result, "a.b") || strings.Contains(result, "axb") {
		t.Fatalf("result=%q err=%v", result, err)
	}
	if !search.ReadOnly() {
		t.Fatal("search missing from read-only tool registry")
	}
}

func TestGrepRejectsEmptyQueryAndBoundsResults(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "data.txt"), []byte(strings.Repeat("match\n", 101)), 0600); err != nil {
		t.Fatal(err)
	}
	workspace := backend.NewFilesystemBackend(&backend.FilesystemBackendConfig{RootDir: root, VirtualMode: true})
	search := &fileSearchTool{backend: workspace, name: "grep"}
	if _, err := search.InvokableRun(context.Background(), `{"query":"","path":"."}`); err == nil {
		t.Fatal("empty query accepted")
	}
	result, err := search.InvokableRun(context.Background(), `{"query":"match","path":"."}`)
	if err != nil || len(strings.Split(result, "\n")) != 100 || strings.Contains(result, ":101:") {
		t.Fatalf("bounded search result=%q err=%v", result, err)
	}
}
