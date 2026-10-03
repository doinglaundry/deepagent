package filesystem

import (
	"context"
	"testing"
)

func newTestLocalFilesystem(t *testing.T, filesystemConfig *LocalFilesystemConfig) *LocalFilesystem {
	t.Helper()
	localFilesystem, err := NewLocalFilesystem(filesystemConfig, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = localFilesystem.Close(context.Background()) })
	return localFilesystem
}
