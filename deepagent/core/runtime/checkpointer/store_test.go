package checkpointer

import (
	"context"
	"errors"
	"testing"
)

type memoryStore struct {
	data map[string][]byte
	err  error
}

func (s *memoryStore) Get(_ context.Context, id string) ([]byte, bool, error) {
	v, ok := s.data[id]
	return v, ok, s.err
}
func (s *memoryStore) Set(_ context.Context, id string, value []byte) error {
	if s.err != nil {
		return s.err
	}
	if s.data == nil {
		s.data = make(map[string][]byte)
	}
	s.data[id] = value
	return nil
}
func TestEnvelopeValidatesIdentityAndRoundTripsSnapshot(t *testing.T) {
	ctx := context.Background()
	raw := &memoryStore{}
	store := New(raw, "thread", "run", "graph-v1")
	if err := store.Set(ctx, "checkpoint", []byte("eino snapshot")); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.Get(ctx, "checkpoint")
	if err != nil || !ok || string(got) != "eino snapshot" {
		t.Fatalf("%s %v %v", got, ok, err)
	}
	for _, other := range []*Store{New(raw, "other", "run", "graph-v1"), New(raw, "thread", "other", "graph-v1"), New(raw, "thread", "run", "graph-v2")} {
		if _, _, err := other.Get(ctx, "checkpoint"); err == nil {
			t.Fatal("accepted incompatible checkpoint")
		}
	}
}
func TestCheckpointStoreFailurePropagates(t *testing.T) {
	sentinel := errors.New("storage unavailable")
	store := New(&memoryStore{err: sentinel}, "thread", "run", "v1")
	if err := store.Set(context.Background(), "id", []byte("snapshot")); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}
