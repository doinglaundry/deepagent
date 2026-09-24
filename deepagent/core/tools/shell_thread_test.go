package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"eino-cli/deepagent/core/backend"
	einotool "github.com/cloudwego/eino/components/tool"
)

func TestShellJobCanBeAwaitedAfterStartingRunContextEnds(t *testing.T) {
	workspace, err := backend.NewLocalFilesystem(&backend.FilesystemBackendConfig{RootDir: t.TempDir(), VirtualMode: true}, "thread")
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close(context.Background())
	items := NewCommandTools(workspace)
	shell := items[1].(einotool.InvokableTool)
	await := items[2].(einotool.InvokableTool)
	ctx, cancel := context.WithCancel(context.Background())
	output, err := shell.InvokableRun(ctx, `{"command":"sleep 0.1; printf finished","timeout_ms":10}`)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimPrefix(strings.Fields(output)[0], "task_id=")
	cancel()
	result, err := await.InvokableRun(context.Background(), fmt.Sprintf(`{"task_id":%q,"timeout_ms":1000}`, id))
	if err != nil || !strings.Contains(result, "status=done") || !strings.Contains(result, "finished") {
		t.Fatalf("await=%q err=%v", result, err)
	}
}
