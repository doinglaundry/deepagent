package backend

import (
	"context"
	"fmt"
	"os"
)

func NewLocalFilesystem(cfg *FilesystemBackendConfig, threadID string) (*LocalFilesystem, error) {
	if cfg == nil || threadID == "" {
		return nil, fmt.Errorf("local filesystem config and thread ID are required")
	}
	b := NewFilesystemBackend(cfg)
	info, err := os.Stat(b.Root())
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace root is not a directory")
	}
	b.commands = NewCommands(threadID, b)
	return b, nil
}

func (b *LocalFilesystem) Execute(ctx context.Context, req CommandRequest) (*CommandResult, error) {
	return b.commands.Execute(ctx, req)
}
func (b *LocalFilesystem) Start(ctx context.Context, req CommandRequest) (string, error) {
	return b.commands.Start(ctx, req)
}
func (b *LocalFilesystem) Wait(ctx context.Context, id, pattern string, offset int) (*CommandSnapshot, error) {
	return b.commands.Wait(ctx, id, pattern, offset)
}
func (b *LocalFilesystem) Cancel(ctx context.Context, id string) error {
	return b.commands.Cancel(ctx, id)
}
func (b *LocalFilesystem) Close(ctx context.Context) error { return b.commands.Close(ctx) }

var _ Filesystem = (*LocalFilesystem)(nil)
var _ CommandService = (*LocalFilesystem)(nil)
var _ ToolWorkspace = (*LocalFilesystem)(nil)
