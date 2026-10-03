package tools

import (
	"context"
	"errors"
	"testing"

	filesystempkg "eino-cli/deepagent/graph/filesystem"

	einotool "github.com/cloudwego/eino/components/tool"
)

type diagnosticsCommands struct {
	filesystempkg.CommandService
	request filesystempkg.CommandRequest
	err     error
}

func (c *diagnosticsCommands) Execute(_ context.Context, request filesystempkg.CommandRequest) (*filesystempkg.CommandResult, error) {
	c.request = request
	return &filesystempkg.CommandResult{ExitCode: 1, Output: "diagnostic"}, c.err
}

func TestReadLintsUsesCommandServiceAndPropagatesCancellation(t *testing.T) {
	workspace := mustLocalFilesystem(t, &filesystempkg.LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true})
	commands := &diagnosticsCommands{}
	lints, err := NewReadLintsTool(workspace, commands)
	if err != nil {
		t.Fatal(err)
	}
	if !lints.RequiresApproval {
		t.Fatal("project tests bypass approval")
	}
	out, err := lints.Tool.(einotool.InvokableTool).InvokableRun(context.Background(), `{}`)
	if err != nil || out != "diagnostic" || commands.request.Command != "go test './...'" || commands.request.MaxOutputBytes != 64<<10 {
		t.Fatalf("out=%q err=%v request=%+v", out, err, commands.request)
	}
	commands.err = context.Canceled
	_, invokableRunErr := lints.Tool.(einotool.InvokableTool).InvokableRun(context.Background(), `{}`)
	if !errors.Is(invokableRunErr, context.Canceled) {
		t.Fatalf("cancellation swallowed: %v", invokableRunErr)
	}
}
