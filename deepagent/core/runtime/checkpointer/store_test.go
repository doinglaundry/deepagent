package checkpointer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

type memoryStore struct {
	data map[string][]byte
	err  error
}

func TestLegacyMigrationPreservesMiddlewareStateAndPublishesEnvelope(t *testing.T) {
	raw, err := os.ReadFile("testdata/legacy_before_model.json")
	if err != nil {
		t.Fatal(err)
	}
	inner := &memoryStore{data: map[string][]byte{"fixture": raw, "deepagent_graph_state_:fixture": []byte(`{"plan":"{\"step\":2}"}`)}}
	store := New(inner, "thread", "run", "core-graph-v1")
	converted, exists, err := store.Get(context.Background(), "fixture")
	if err != nil || !exists {
		t.Fatalf("exists=%v err=%v", exists, err)
	}
	var envelope Envelope
	if err := json.Unmarshal(inner.data["fixture"], &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Version != 1 || string(envelope.EinoSnapshot) != string(converted) {
		t.Fatal("migration did not publish envelope")
	}
	var cp legacyValue
	if err := json.Unmarshal(converted, &cp); err != nil {
		t.Fatal(err)
	}
	var state struct{ Extensions map[string]json.RawMessage }
	if err := json.Unmarshal(cp.MapValues["State"].JSONValue, &state); err != nil {
		t.Fatal(err)
	}
	var plan string
	if err := json.Unmarshal(state.Extensions["middleware:plan"], &plan); err != nil || plan != `{"step":2}` {
		t.Fatalf("plan=%q err=%v", plan, err)
	}
}

func TestLegacyMigrationRejectsUnknownToolBoundary(t *testing.T) {
	raw, err := os.ReadFile("testdata/legacy_before_model.json")
	if err != nil {
		t.Fatal(err)
	}
	var cp legacyValue
	if err := json.Unmarshal(raw, &cp); err != nil {
		t.Fatal(err)
	}
	inputs := cp.MapValues["Inputs"].MapValues
	inputs[`"tools"`] = inputs[`"model"`]
	delete(inputs, `"model"`)
	raw, err = json.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	inner := &memoryStore{data: map[string][]byte{"fixture": raw}}
	if _, _, err := New(inner, "thread", "run", "core-graph-v1").Get(context.Background(), "fixture"); err == nil {
		t.Fatal("unsupported tool state silently resumed")
	}
	if string(inner.data["fixture"]) != string(raw) {
		t.Fatal("rejected migration overwrote original checkpoint")
	}
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
