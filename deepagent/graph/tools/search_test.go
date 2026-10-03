package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	filesystempkg "eino-cli/deepagent/graph/filesystem"

	einotool "github.com/cloudwego/eino/components/tool"
)

func TestGrepPreservesLiteralQueryAlias(t *testing.T) {
	root := t.TempDir()
	writeErr := os.WriteFile(filepath.Join(root, "data.txt"), []byte("a.b\naxb\n"), 0600)
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	workspace := mustLocalFilesystem(t, &filesystempkg.LocalFilesystemConfig{RootDir: root, VirtualMode: true})
	search := &fileSearchTool{backend: workspace, name: "grep"}
	result, err := search.InvokableRun(context.Background(), `{"query":"a.b","path":"."}`)
	if err != nil || !strings.Contains(result, "a.b") || strings.Contains(result, "axb") {
		t.Fatalf("result=%q err=%v", result, err)
	}
	if !search.ReadOnly() {
		t.Fatal("search missing from read-only tool set")
	}
}

func TestGrepRejectsEmptyQueryAndBoundsResults(t *testing.T) {
	root := t.TempDir()
	writeErr := os.WriteFile(filepath.Join(root, "data.txt"), []byte(strings.Repeat("match\n", 101)), 0600)
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	workspace := mustLocalFilesystem(t, &filesystempkg.LocalFilesystemConfig{RootDir: root, VirtualMode: true})
	search := &fileSearchTool{backend: workspace, name: "grep"}
	_, invokableRunErr := search.InvokableRun(context.Background(), `{"query":"","path":"."}`)
	if invokableRunErr == nil {
		t.Fatal("empty query accepted")
	}
	result, err := search.InvokableRun(context.Background(), `{"query":"match","path":"."}`)
	if err != nil || len(strings.Split(result, "\n")) != 100 || strings.Contains(result, ":101:") {
		t.Fatalf("bounded search result=%q err=%v", result, err)
	}
}

func TestSemanticSearchUsesWorkspaceBoundary(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeErr2 := os.WriteFile(filepath.Join(root, "lease.go"), []byte("func claimLease() {}"), 0600)
	if writeErr2 != nil {
		t.Fatal(writeErr2)
	}
	writeErr := os.WriteFile(filepath.Join(outside, "secret.go"), []byte("private secret"), 0600)
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	symlinkErr := os.Symlink(outside, filepath.Join(root, "escape"))
	if symlinkErr != nil {
		t.Fatal(symlinkErr)
	}
	workspace := mustLocalFilesystem(t, &filesystempkg.LocalFilesystemConfig{RootDir: root, VirtualMode: true})
	search, err := NewSemanticSearchTool(workspace)
	if err != nil {
		t.Fatal(err)
	}
	text, err := search.(einotool.InvokableTool).InvokableRun(context.Background(), `{"query":"claim lease"}`)
	if err != nil || !strings.Contains(text, "lease.go:1:") {
		t.Fatalf("text=%q err=%v", text, err)
	}
	invokableRunText, invokableRunErr := search.(einotool.InvokableTool).InvokableRun(context.Background(), `{"query":"secret","path":"escape"}`)
	if invokableRunErr == nil {
		t.Fatalf("search escaped workspace: %q", invokableRunText)
	}
}
