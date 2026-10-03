package middleware

import (
	"context"
	"testing"

	filesystempkg "eino-cli/deepagent/graph/filesystem"
)

func newTestLocalFilesystem(t *testing.T, config *filesystempkg.LocalFilesystemConfig) *filesystempkg.LocalFilesystem {
	t.Helper()
	filesystem, err := filesystempkg.NewLocalFilesystem(config, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = filesystem.Close(context.Background()) })
	return filesystem
}
