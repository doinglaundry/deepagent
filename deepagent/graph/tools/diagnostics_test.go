package tools

import (
	"context"
	"errors"
	"testing"

	filesystempkg "eino-cli/deepagent/graph/filesystem"
	agentmodel "eino-cli/deepagent/model"

	einotool "github.com/cloudwego/eino/components/tool"
)

type diagnosticsCommands struct {
	agentmodel.CommandService
	request agentmodel.CommandRequest
	err     error
}

func (commandService *diagnosticsCommands) Execute(_ context.Context, request agentmodel.CommandRequest) (*agentmodel.CommandResult, error) {
	commandService.request = request
	return &agentmodel.CommandResult{ExitCode: 1, Output: "diagnostic"}, commandService.err
}

func TestReadLintsUsesCommandServiceAndPropagatesCancellation(t *testing.T) {
	filesystem := newTestLocalFilesystem(t, &filesystempkg.LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true})
	commandService := &diagnosticsCommands{}
	lintDescriptor, err := NewReadLintsTool(filesystem, commandService)
	if err != nil {
		t.Fatal(err)
	}
	if !lintDescriptor.RequiresApproval {
		t.Fatal("project tests bypass approval")
	}
	output, err := lintDescriptor.Tool.(einotool.InvokableTool).InvokableRun(context.Background(), `{}`)
	if err != nil || output != "diagnostic" || commandService.request.Command != "go test './...'" || commandService.request.MaxOutputBytes != 64<<10 {
		t.Fatalf("out=%q err=%v request=%+v", output, err, commandService.request)
	}
	commandService.err = context.Canceled
	_, invokableRunErr := lintDescriptor.Tool.(einotool.InvokableTool).InvokableRun(context.Background(), `{}`)
	if !errors.Is(invokableRunErr, context.Canceled) {
		t.Fatalf("cancellation swallowed: %v", invokableRunErr)
	}
}
