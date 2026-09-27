package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eino-cli/deepagent/core/backend"
	einotool "github.com/cloudwego/eino/components/tool"
)

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
