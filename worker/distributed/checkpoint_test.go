package distributed

import (
	"context"
	"eino-cli/manager"
	"testing"
)

func TestFileCheckpointsAreNamespaceScopedAndSurviveReopen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	c := Config{Manager: manager.Config{Namespace: "a"}, Checkpoint: CheckpointConfig{Backend: "file", Path: root}}
	store, close, err := newCheckpointStore(ctx, manager.NewMemory("a"), c)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Set(ctx, "checkpoint", []byte("state")); err != nil {
		t.Fatal(err)
	}
	close()
	reopened, close, err := newCheckpointStore(ctx, manager.NewMemory("a"), c)
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	data, err := reopened.Get(ctx, "checkpoint")
	if err != nil || string(data) != "state" {
		t.Fatalf("checkpoint %q err %v", data, err)
	}
	c.Manager.Namespace = "b"
	other, closeOther, err := newCheckpointStore(ctx, manager.NewMemory("b"), c)
	if err != nil {
		t.Fatal(err)
	}
	defer closeOther()
	if _, err = other.Get(ctx, "checkpoint"); err == nil {
		t.Fatal("read foreign namespace checkpoint")
	}
}
