package types

import (
	"encoding/json"
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

func TestGraphStateExtensionPersistenceBoundary(t *testing.T) {
	source := NewGraphState()
	source.RegisterStateful("saved", &stateFixture{value: "checkpoint-value"})
	source.RegisterRuntimeOnlyStateful("local", &stateFixture{value: "must-not-persist"})
	snapshot := &RunState{}
	err := source.SnapshotExtensions(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	_, hasLocal := snapshot.Extensions["middleware:local"]
	if hasLocal {
		t.Fatal("runtime-only state persisted")
	}
	target := NewGraphState()
	saved, local := &stateFixture{}, &stateFixture{value: "fresh-local"}
	target.RegisterStateful("saved", saved)
	target.RegisterStateful("local", local)
	err = target.RestoreExtensions(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if saved.value != "checkpoint-value" || local.value != "fresh-local" {
		t.Fatalf("saved=%q local=%q", saved.value, local.value)
	}
	runtime := &stateFixture{value: "keep-runtime"}
	target.RegisterRuntimeOnlyStateful("saved", runtime)
	err = target.RestoreExtensions(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.value != "keep-runtime" {
		t.Fatal("runtime-only state overwritten")
	}
	if target.GetStateful("saved") != runtime || target.GetStateful("missing") != nil {
		t.Fatal("registry lookup mismatch")
	}
}

func TestGraphStateExtensionRestoreErrors(t *testing.T) {
	failure := errors.New("restore failed")
	for _, tc := range []struct {
		name       string
		raw        json.RawMessage
		restoreErr error
		wantError  bool
	}{
		{name: "missing"},
		{name: "invalid snapshot", raw: json.RawMessage(`{`), wantError: true},
		{name: "invalid value", raw: json.RawMessage(`17`), wantError: true},
		{name: "component rejects state", raw: json.RawMessage(`"new"`), restoreErr: failure, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := NewGraphState()
			value := &stateFixture{value: "original", restoreErr: tc.restoreErr}
			registry.RegisterStateful("saved", value)
			snapshot := &RunState{Extensions: map[string]json.RawMessage{"middleware:unknown": json.RawMessage(`"ignored"`)}}
			if tc.raw != nil {
				snapshot.Extensions["middleware:saved"] = tc.raw
			}
			err := registry.RestoreExtensions(snapshot)
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v want=%v", err, tc.wantError)
			}
			if value.value != "original" {
				t.Fatalf("value mutated: %q", value.value)
			}
		})
	}
}
