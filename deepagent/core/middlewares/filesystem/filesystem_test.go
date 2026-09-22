package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"eino-cli/deepagent/core/backends"
	"eino-cli/deepagent/core/middlewares/execute"
	einofs "github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/components/tool"
)

type commandExecutorProbe struct{ calls int }

func (p *commandExecutorProbe) Execute(context.Context, string) (*backends.ExecuteResponse, error) {
	p.calls++
	return &backends.ExecuteResponse{Output: "ok"}, nil
}

func (p *commandExecutorProbe) ExecuteCommand(context.Context, backends.CommandRequest) (*backends.CommandResult, error) {
	p.calls++
	return &backends.CommandResult{Output: "ok"}, nil
}

func TestToolsUseCanonicalBackend(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := backends.NewFilesystemBackend(&backends.FilesystemBackendConfig{RootDir: root, VirtualMode: true})
	middleware := New(&FilesystemConfig{Backend: backend})
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
	executor := &commandExecutorProbe{}
	shell := shellAdapter{executor: executor, classifier: execute.NewDefaultClassifier()}
	if _, err := shell.Execute(context.Background(), &einofs.ExecuteRequest{Command: "ls"}); err != nil {
		t.Fatal(err)
	}
	if _, err := shell.Execute(context.Background(), &einofs.ExecuteRequest{Command: "rm file"}); err == nil {
		t.Fatal("dangerous command was executed")
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls=%d", executor.calls)
	}
}

func TestReadOnlyFilesystemOmitsMutations(t *testing.T) {
	backend := backends.NewFilesystemBackend(&backends.FilesystemBackendConfig{RootDir: t.TempDir(), VirtualMode: true})
	items, err := New(&FilesystemConfig{Backend: backend, ReadOnly: true}).Tools(context.Background())
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
	*backends.FilesystemBackend
	patch string
}

func (p *applyPatchProbe) SupportsApplyPatch() bool { return true }
func (p *applyPatchProbe) ApplyPatch(_ context.Context, patch string) (string, error) {
	p.patch = patch
	return "patched", nil
}

func TestApplyPatchIsExposedByCapableBackend(t *testing.T) {
	backend := &applyPatchProbe{FilesystemBackend: backends.NewFilesystemBackend(&backends.FilesystemBackendConfig{RootDir: t.TempDir(), VirtualMode: true})}
	items, err := New(&FilesystemConfig{Backend: backend}).Tools(context.Background())
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
	backend := backends.NewSandboxFilesystemBackend(&backends.FilesystemBackendConfig{RootDir: t.TempDir(), VirtualMode: true})
	items, err := New(&FilesystemConfig{Backend: backend}).Tools(context.Background())
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
	backend := backends.NewFilesystemBackend(&backends.FilesystemBackendConfig{RootDir: root, VirtualMode: true})
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
	result, err := semanticSearch(root, "", "claim lease")
	if err != nil || !strings.Contains(result, "manager.go:1") {
		t.Fatalf("semanticSearch = %q, %v", result, err)
	}
}

func TestBackgroundShellCanBeAwaited(t *testing.T) {
	job, done, err := startShellJob("printf ready", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shell job did not finish")
	}
	result := formatShellSnapshot(job, 0)
	if !strings.Contains(result, "done exit_code=0") || !strings.Contains(result, "ready") {
		t.Fatalf("snapshot = %q", result)
	}
}
