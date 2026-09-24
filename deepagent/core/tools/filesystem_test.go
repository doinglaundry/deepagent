package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eino-cli/deepagent/core/backend"
	einotool "github.com/cloudwego/eino/components/tool"
)

func TestFilesystemToolsNamesAndArgumentAliases(t *testing.T) {
	ctx := context.Background()
	b, err := backend.NewLocalFilesystem(&backend.FilesystemBackendConfig{RootDir: t.TempDir(), VirtualMode: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close(ctx)
	registered := map[string]einotool.InvokableTool{}
	items, err := NewWorkspaceTools(b, WorkspaceToolOptions{EnableCommands: true, EnablePatch: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range items {
		info, err := tool.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		registered[info.Name] = tool.(einotool.InvokableTool)
	}
	for _, name := range []string{"list_files", "ls", "read_file", "write_file", "edit_file", "delete_file", "glob", "grep"} {
		if registered[name] == nil {
			t.Fatalf("missing %s", name)
		}
	}
	for _, name := range []string{"write_file", "edit_file", "delete_file"} {
		approval, ok := registered[name].(interface{ RequiresApproval() bool })
		if !ok || !approval.RequiresApproval() {
			t.Fatalf("%s must request approval", name)
		}
	}
	for _, step := range []struct{ name, args string }{
		{"write_file", `{"file_path":"a.txt","content":"first\nsecond\n"}`},
		{"edit_file", `{"file_path":"a.txt","old_string":"second","new_string":"changed"}`},
	} {
		if _, err := registered[step.name].InvokableRun(ctx, step.args); err != nil {
			t.Fatal(err)
		}
	}
	text, err := registered["read_file"].InvokableRun(ctx, `{"file_path":"a.txt","offset":2,"limit":1}`)
	if err != nil || !strings.Contains(text, "changed") || strings.Contains(text, "first") {
		t.Fatalf("read=%q err=%v", text, err)
	}
	if _, err := registered["delete_file"].InvokableRun(ctx, `{"file_path":"a.txt"}`); err != nil {
		t.Fatal(err)
	}
}

func TestFilesystemPreservesWorkerReadAndExactEditContracts(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	b := backend.NewFilesystemBackend(&backend.FilesystemBackendConfig{RootDir: root, VirtualMode: true, MaxFileSizeMB: 1})
	read := NewReadFileTool(b).(einotool.InvokableTool)
	edit := NewEditFileTool(b).(einotool.InvokableTool)
	if err := os.WriteFile(filepath.Join(root, "data.txt"), []byte("hello world"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := edit.InvokableRun(ctx, `{"path":"data.txt","old":"world","new":"Go"}`); err != nil {
		t.Fatal(err)
	}
	if result, err := read.InvokableRun(ctx, `{"path":"data.txt"}`); err != nil || result != "hello Go" {
		t.Fatalf("read=%q err=%v", result, err)
	}
	for _, old := range []string{"missing", ""} {
		if _, err := edit.InvokableRun(ctx, `{"path":"data.txt","old":"`+old+`","new":"bad"}`); err == nil {
			t.Fatalf("invalid edit accepted: old=%q", old)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "data.txt"), []byte("twice twice"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := edit.InvokableRun(ctx, `{"path":"data.txt","old":"twice","new":"bad"}`); err == nil {
		t.Fatal("ambiguous edit accepted")
	}
	if result, err := read.InvokableRun(ctx, `{"path":"data.txt"}`); err != nil || result != "twice twice" {
		t.Fatalf("rejected edit changed file: %q %v", result, err)
	}
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(strings.Repeat("x", (1<<20)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := read.InvokableRun(ctx, `{"path":"large.txt"}`); err == nil {
		t.Fatal("oversized file accepted")
	}
}
