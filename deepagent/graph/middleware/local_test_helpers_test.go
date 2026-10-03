package middleware

import (
	"context"
	"testing"

	filesystempkg "eino-cli/deepagent/graph/filesystem"
)

func mustLocalFilesystem(t *testing.T, cfg *filesystempkg.LocalFilesystemConfig) *filesystempkg.LocalFilesystem {
	t.Helper()
	filesystem, err := filesystempkg.NewLocalFilesystem(cfg, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = filesystem.Close(context.Background()) })
	return filesystem
}
