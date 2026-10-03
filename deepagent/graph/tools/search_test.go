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
	workspaceRoot := t.TempDir()
	writeErr := os.WriteFile(filepath.Join(workspaceRoot, "data.txt"), []byte("a.b\naxb\n"), 0600)
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	filesystem := newTestLocalFilesystem(t, &filesystempkg.LocalFilesystemConfig{RootDir: workspaceRoot, VirtualMode: true})
	searchTool := &fileSearchTool{filesystem: filesystem, toolName: "grep"}
	result, err := searchTool.InvokableRun(context.Background(), `{"query":"a.b","path":"."}`)
	if err != nil || !strings.Contains(result, "a.b") || strings.Contains(result, "axb") {
		t.Fatalf("result=%q err=%v", result, err)
	}
}

func TestGrepRejectsEmptyQueryAndBoundsResults(t *testing.T) {
	workspaceRoot := t.TempDir()
	writeErr := os.WriteFile(filepath.Join(workspaceRoot, "data.txt"), []byte(strings.Repeat("match\n", 101)), 0600)
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	filesystem := newTestLocalFilesystem(t, &filesystempkg.LocalFilesystemConfig{RootDir: workspaceRoot, VirtualMode: true})
	searchTool := &fileSearchTool{filesystem: filesystem, toolName: "grep"}
	_, emptyQueryErr := searchTool.InvokableRun(context.Background(), `{"query":"","path":"."}`)
	if emptyQueryErr == nil {
		t.Fatal("empty query accepted")
	}
	result, err := searchTool.InvokableRun(context.Background(), `{"query":"match","path":"."}`)
	if err != nil || len(strings.Split(result, "\n")) != 100 || strings.Contains(result, ":101:") {
		t.Fatalf("bounded search result=%q err=%v", result, err)
	}
}

func TestSemanticSearchUsesWorkspaceBoundary(t *testing.T) {
	workspaceRoot, outsideRoot := t.TempDir(), t.TempDir()
	workspaceWriteErr := os.WriteFile(filepath.Join(workspaceRoot, "lease.go"), []byte("func claimLease() {}"), 0600)
	if workspaceWriteErr != nil {
		t.Fatal(workspaceWriteErr)
	}
	outsideWriteErr := os.WriteFile(filepath.Join(outsideRoot, "secret.go"), []byte("private secret"), 0600)
	if outsideWriteErr != nil {
		t.Fatal(outsideWriteErr)
	}
	symlinkErr := os.Symlink(outsideRoot, filepath.Join(workspaceRoot, "escape"))
	if symlinkErr != nil {
		t.Fatal(symlinkErr)
	}
	filesystem := newTestLocalFilesystem(t, &filesystempkg.LocalFilesystemConfig{RootDir: workspaceRoot, VirtualMode: true})
	semanticSearchDescriptor, err := NewSemanticSearchTool(filesystem)
	if err != nil {
		t.Fatal(err)
	}
	searchOutput, err := semanticSearchDescriptor.Tool.(einotool.InvokableTool).InvokableRun(context.Background(), `{"query":"claim lease"}`)
	if err != nil || !strings.Contains(searchOutput, "lease.go:1:") {
		t.Fatalf("text=%q err=%v", searchOutput, err)
	}
	outsideSearchOutput, outsideSearchErr := semanticSearchDescriptor.Tool.(einotool.InvokableTool).InvokableRun(context.Background(), `{"query":"secret","path":"escape"}`)
	if outsideSearchErr == nil {
		t.Fatalf("search escaped workspace: %q", outsideSearchOutput)
	}
}
