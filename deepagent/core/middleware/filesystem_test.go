package middleware

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"eino-cli/deepagent/core/backend"
	"eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/components/tool"
)

func TestToolsUseCanonicalBackend(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend, err := backend.NewLocalFilesystem(&backend.FilesystemBackendConfig{RootDir: root, VirtualMode: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close(context.Background())
	middleware := NewFilesystem(&FilesystemConfig{Workspace: backend})
	items, err := middleware.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]tool.InvokableTool{}
	for _, item := range items {
		info, infoErr := item.Info(context.Background())
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		if invokable, ok := item.(tool.InvokableTool); ok {
			byName[info.Name] = invokable
		}
	}
	reader := byName["read_file"]
	if reader == nil {
		t.Fatalf("read_file missing from %d tools", len(items))
	}
	output, err := reader.InvokableRun(context.Background(), `{"file_path":"a.txt"}`)
	if err != nil || output == "" {
		t.Fatalf("read_file = %q, %v", output, err)
	}
}

func TestShellExecutesOnlyClassifiedSafeCommands(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workspace := backend.NewFilesystemBackend(&backend.FilesystemBackendConfig{RootDir: root, VirtualMode: true})
	service := backend.NewCommands("thread", workspace)
	defer service.Close(ctx)
	commands := classifiedCommands{CommandService: service}
	if _, err := commands.Execute(ctx, backend.CommandRequest{Command: "ls"}); err != nil {
		t.Fatal(err)
	}
	if _, err := commands.Execute(ctx, backend.CommandRequest{Command: "rm file"}); err == nil {
		t.Fatal("dangerous command was executed")
	}
}

func TestReadOnlyFilesystemOmitsMutations(t *testing.T) {
	backend, err := backend.NewLocalFilesystem(&backend.FilesystemBackendConfig{RootDir: t.TempDir(), VirtualMode: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close(context.Background())
	items, err := NewFilesystem(&FilesystemConfig{Workspace: backend, ReadOnly: true}).Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		info, _ := item.Info(context.Background())
		if info.Name == "write_file" || info.Name == "edit_file" || info.Name == "execute" {
			t.Fatalf("read-only middleware exposed %q", info.Name)
		}
	}
}

type applyPatchProbe struct {
	*backend.LocalFilesystem
	patch string
}

func (p *applyPatchProbe) SupportsApplyPatch() bool { return true }
func (p *applyPatchProbe) ApplyPatch(_ context.Context, patch string) (string, error) {
	p.patch = patch
	return "patched", nil
}

func TestApplyPatchIsExposedByCapableBackend(t *testing.T) {
	fs, err := backend.NewLocalFilesystem(&backend.FilesystemBackendConfig{RootDir: t.TempDir(), VirtualMode: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close(context.Background())
	backend := &applyPatchProbe{LocalFilesystem: fs}
	items, err := NewFilesystem(&FilesystemConfig{Workspace: backend}).Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		info, _ := item.Info(context.Background())
		if info.Name != "apply_patch" {
			continue
		}
		invokable, ok := item.(tool.InvokableTool)
		if !ok {
			t.Fatal("apply_patch is not invokable")
		}
		output, err := invokable.InvokableRun(context.Background(), `{"patch":"*** Begin Patch\\n*** End Patch"}`)
		if err != nil || output != "patched" || backend.patch == "" {
			t.Fatalf("apply_patch = %q, %v, patch=%q", output, err, backend.patch)
		}
		return
	}
	t.Fatal("apply_patch tool missing")
}

func TestWorkspaceToolsAreExposed(t *testing.T) {
	backend, err := backend.NewLocalFilesystem(&backend.FilesystemBackendConfig{RootDir: t.TempDir(), VirtualMode: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close(context.Background())
	items, err := NewFilesystem(&FilesystemConfig{Workspace: backend}).Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"execute": true, "delete_file": true, "rg": true, "semantic_search": true, "read_lints": true, "shell": true, "await_shell": true}
	for _, item := range items {
		info, _ := item.Info(context.Background())
		delete(want, info.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing workspace tools: %v", want)
	}
}

func TestDeleteFileRefusesDirectories(t *testing.T) {
	root := t.TempDir()
	backend := backend.NewFilesystemBackend(&backend.FilesystemBackendConfig{RootDir: root, VirtualMode: true})
	if _, err := backend.DeleteFile(context.Background(), "."); err == nil {
		t.Fatal("DeleteFile accepted a directory")
	}
	path := filepath.Join(root, "remove.txt")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.DeleteFile(context.Background(), "remove.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file still exists: %v", err)
	}
}

func TestSemanticSearchRanksWorkspaceContent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "manager.go"), []byte("func ClaimLease() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	search, err := tools.NewSemanticSearchTool(backend.NewFilesystemBackend(&backend.FilesystemBackendConfig{RootDir: root, VirtualMode: true}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := search.(tool.InvokableTool).InvokableRun(context.Background(), `{"query":"claim lease"}`)
	if err != nil || !strings.Contains(result, "manager.go:1") {
		t.Fatalf("semanticSearch = %q, %v", result, err)
	}
}

func TestBackgroundShellCanBeAwaited(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	service := backend.NewCommands("thread", backend.NewFilesystemBackend(&backend.FilesystemBackendConfig{RootDir: t.TempDir(), VirtualMode: true}))
	defer service.Close(context.Background())
	id, err := service.Start(ctx, backend.CommandRequest{Command: "printf ready"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Wait(ctx, id, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Done || snapshot.ExitCode != 0 || snapshot.Output != "ready" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}
