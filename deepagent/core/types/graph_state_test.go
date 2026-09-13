package types

import (
	"context"
	"errors"
	"testing"
)

type stateFixture struct {
	value      string
	restoreErr error
}

func (s *stateFixture) MarshalRuntimeState() string { return s.value }
func (s *stateFixture) UnmarshalRuntimeState(data string) error {
	if s.restoreErr != nil {
		return s.restoreErr
	}
	s.value = data
	return nil
}

type checkpointFixture struct {
	data           map[string][]byte
	getErr, setErr error
}

func (s *checkpointFixture) Set(_ context.Context, key string, data []byte) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.data[key] = append([]byte(nil), data...)
	return nil
}
func (s *checkpointFixture) Get(_ context.Context, key string) ([]byte, bool, error) {
	data, exists := s.data[key]
	return data, exists, s.getErr
}

func TestGraphStatePersistenceBoundary(t *testing.T) {
	ctx := context.Background()
	store := &checkpointFixture{data: map[string][]byte{}}
	source := NewGraphState(store)
	source.RegisterStateful("saved", &stateFixture{value: "checkpoint-value"})
	source.RegisterRuntimeOnlyStateful("local", &stateFixture{value: "must-not-persist"})
	if err := source.Save(ctx, "run-a"); err != nil {
		t.Fatal(err)
	}
	// Restore the same name as persistent to detect accidental serialization
	// of an entry that was runtime-only in the source.
	target := NewGraphState(store)
	saved, local := &stateFixture{}, &stateFixture{value: "fresh-local"}
	target.RegisterStateful("saved", saved)
	target.RegisterStateful("local", local)
	if err := target.Resume(ctx, "run-a"); err != nil {
		t.Fatal(err)
	}
	if saved.value != "checkpoint-value" || local.value != "fresh-local" {
		t.Fatalf("unexpected restored values: saved=%q local=%q", saved.value, local.value)
	}
	// A runtime-only destination must also ignore a persistent snapshot.
	target = NewGraphState(store)
	runtime := &stateFixture{value: "keep-runtime"}
	target.RegisterRuntimeOnlyStateful("saved", runtime)
	if err := target.Resume(ctx, "run-a"); err != nil {
		t.Fatal(err)
	}
	if runtime.value != "keep-runtime" {
		t.Fatal("runtime-only state was overwritten")
	}
	if err := target.Resume(ctx, "other-run"); err != nil {
		t.Fatal(err)
	}
}

func TestGraphStateResumeErrors(t *testing.T) {
	failure := errors.New("restore failed")
	for _, tc := range []struct {
		name               string
		data               []byte
		getErr, restoreErr error
		wantError          bool
	}{
		{name: "missing"},
		{name: "store unavailable", data: []byte(`{"saved":"new"}`), getErr: failure},
		{name: "invalid snapshot", data: []byte(`{`), wantError: true},
		{name: "state rejects snapshot", data: []byte(`{"saved":"new"}`), restoreErr: failure, wantError: true},
		{name: "unknown entry ignored", data: []byte(`{"unknown":"new"}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &checkpointFixture{data: map[string][]byte{}, getErr: tc.getErr}
			if tc.data != nil {
				store.data[graphStateKey("run-a")] = tc.data
			}
			state := NewGraphState(store)
			value := &stateFixture{value: "original", restoreErr: tc.restoreErr}
			state.RegisterStateful("saved", value)
			err := state.Resume(context.Background(), "run-a")
			if (err != nil) != tc.wantError {
				t.Fatalf("Resume error=%v, want error=%v", err, tc.wantError)
			}
			if value.value != "original" {
				t.Fatalf("unexpected mutation: %q", value.value)
			}
		})
	}
}

func TestGraphStateSaveFailureAndNoStore(t *testing.T) {
	ctx := context.Background()
	failure := errors.New("write failed")
	state := NewGraphState(&checkpointFixture{setErr: failure})
	if err := state.Save(ctx, "run-a"); !errors.Is(err, failure) {
		t.Fatalf("Save error=%v", err)
	}
	state = NewGraphState(nil)
	if err := state.Save(ctx, "run-a"); err != nil {
		t.Fatal(err)
	}
	if err := state.Resume(ctx, "run-a"); err != nil {
		t.Fatal(err)
	}
}
