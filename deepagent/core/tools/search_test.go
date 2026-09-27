package tools

import (
	"context"
	"eino-cli/deepagent/core/backend"
	"errors"
	einotool "github.com/cloudwego/eino/components/tool"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrepPreservesLiteralQueryAlias(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "data.txt"), []byte("a.b\naxb\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workspace := mustLocalFilesystem(t, &backend.LocalFilesystemConfig{RootDir: root, VirtualMode: true})
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
	workspace := mustLocalFilesystem(t, &backend.LocalFilesystemConfig{RootDir: root, VirtualMode: true})
	search := &fileSearchTool{backend: workspace, name: "grep"}
	if _, err := search.InvokableRun(context.Background(), `{"query":"","path":"."}`); err == nil {
		t.Fatal("empty query accepted")
	}
	result, err := search.InvokableRun(context.Background(), `{"query":"match","path":"."}`)
	if err != nil || len(strings.Split(result, "\n")) != 100 || strings.Contains(result, ":101:") {
		t.Fatalf("bounded search result=%q err=%v", result, err)
	}
}

func TestSemanticSearchUsesWorkspaceBoundary(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "lease.go"), []byte("func claimLease() {}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.go"), []byte("private secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	workspace := mustLocalFilesystem(t, &backend.LocalFilesystemConfig{RootDir: root, VirtualMode: true})
	search, err := NewSemanticSearchTool(workspace)
	if err != nil {
		t.Fatal(err)
	}
	text, err := search.(einotool.InvokableTool).InvokableRun(context.Background(), `{"query":"claim lease"}`)
	if err != nil || !strings.Contains(text, "lease.go:1:") {
		t.Fatalf("text=%q err=%v", text, err)
	}
	if text, err := search.(einotool.InvokableTool).InvokableRun(context.Background(), `{"query":"secret","path":"escape"}`); err == nil {
		t.Fatalf("search escaped workspace: %q", text)
	}
}

type diagnosticsCommands struct {
	backend.CommandService
	request backend.CommandRequest
	err     error
}

func (c *diagnosticsCommands) Execute(_ context.Context, request backend.CommandRequest) (*backend.CommandResult, error) {
	c.request = request
	return &backend.CommandResult{ExitCode: 1, Output: "diagnostic"}, c.err
}

func TestReadLintsUsesCommandServiceAndPropagatesCancellation(t *testing.T) {
	workspace := mustLocalFilesystem(t, &backend.LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true})
	commands := &diagnosticsCommands{}
	lints, err := NewReadLintsTool(workspace, commands)
	if err != nil {
		t.Fatal(err)
	}
	if !lints.(interface{ RequiresApproval() bool }).RequiresApproval() {
		t.Fatal("project tests bypass approval")
	}
	out, err := lints.(einotool.InvokableTool).InvokableRun(context.Background(), `{}`)
	if err != nil || out != "diagnostic" || commands.request.Command != "go test './...'" || commands.request.MaxOutputBytes != 64<<10 {
		t.Fatalf("out=%q err=%v request=%+v", out, err, commands.request)
	}
	commands.err = context.Canceled
	if _, err := lints.(einotool.InvokableTool).InvokableRun(context.Background(), `{}`); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation swallowed: %v", err)
	}
}
