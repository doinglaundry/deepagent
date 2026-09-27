package deepagents

import (
	"context"
	"testing"

	"eino-cli/deepagent/core/backend"
)

func mustLocalFilesystem(t *testing.T, cfg *backend.LocalFilesystemConfig) *backend.LocalFilesystem {
	t.Helper()
	filesystem, err := backend.NewLocalFilesystem(cfg, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = filesystem.Close(context.Background()) })
	return filesystem
}
