package checkpointer

import (
	"context"
	"testing"
)

func TestFileCheckpointSharedAndKeysConfined(t *testing.T) {
	root := t.TempDir()
	a, e := NewFile(root)
	if e != nil {
		t.Fatal(e)
	}
	b, e := NewFile(root)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	if e = a.Set(ctx, "checkpoint", []byte(`{"run":"same"}`)); e != nil {
		t.Fatal(e)
	}
	v, e := b.Get(ctx, "checkpoint")
	if e != nil || string(v) != `{"run":"same"}` {
		t.Fatal(string(v), e)
	}
	if e = a.Set(ctx, "../../checkpoint", []byte("bad")); e == nil {
		t.Fatal("traversal key accepted")
	}
	if _, e = b.Get(ctx, "other"); e == nil {
		t.Fatal("missing checkpoint accepted")
	}
}
