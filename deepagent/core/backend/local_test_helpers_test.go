package backend

import (
	"context"
	"testing"
)

func mustLocalFilesystem(t *testing.T, cfg *LocalFilesystemConfig) *LocalFilesystem {
	t.Helper()
	filesystem, err := NewLocalFilesystem(cfg, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = filesystem.Close(context.Background()) })
	return filesystem
}
