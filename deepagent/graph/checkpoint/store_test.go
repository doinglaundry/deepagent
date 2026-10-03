package checkpointer

import (
	"context"
	"encoding/json"
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
	setErr := store.Set(ctx, "checkpoint", []byte("eino snapshot"))
	if setErr != nil {
		t.Fatal(setErr)
	}
	got, ok, err := store.Get(ctx, "checkpoint")
	if err != nil || !ok || string(got) != "eino snapshot" {
		t.Fatalf("%s %v %v", got, ok, err)
	}
	for _, other := range []*Store{New(raw, "other", "run", "graph-v1"), New(raw, "thread", "other", "graph-v1"), New(raw, "thread", "run", "graph-v2")} {
		_, _, err := other.Get(ctx, "checkpoint")
		if err == nil {
			t.Fatal("accepted incompatible checkpoint")
		}
	}
}

func TestCheckpointStoreFailurePropagates(t *testing.T) {
	sentinel := errors.New("storage unavailable")
	store := New(&memoryStore{err: sentinel}, "thread", "run", "v1")
	setErr := store.Set(context.Background(), "id", []byte("snapshot"))
	if !errors.Is(setErr, sentinel) {
		t.Fatal(setErr)
	}
}

func TestFreshCheckpointReplacesForeignIdentityAndInvalidBytes(t *testing.T) {
	for _, existing := range [][]byte{
		[]byte(`obsolete bytes`),
		[]byte(`{"Version":1,"ThreadID":"thread","RunID":"old","GraphVersion":"graph-v1","EinoSnapshot":"b2xk","OldOwnerField":true}`),
	} {
		inner := &memoryStore{data: map[string][]byte{"checkpoint": existing}}
		store := New(inner, "thread", "fresh", "graph-v1")
		err := store.Set(context.Background(), "checkpoint", []byte("fresh snapshot"))
		if err != nil {
			t.Fatal(err)
		}
		got, exists, err := store.Get(context.Background(), "checkpoint")
		if err != nil || !exists || string(got) != "fresh snapshot" {
			t.Fatalf("snapshot=%s exists=%v err=%v", got, exists, err)
		}
		var envelope map[string]json.RawMessage
		err = json.Unmarshal(inner.data["checkpoint"], &envelope)
		if err != nil {
			t.Fatal(err)
		}
		_, oldField := envelope["OldOwnerField"]
		if oldField {
			t.Fatal("foreign execution metadata retained")
		}
	}
}
