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

func (stateFixture *stateFixture) MarshalRuntimeState() string { return stateFixture.value }
func (stateFixture *stateFixture) UnmarshalRuntimeState(data string) error {
	if stateFixture.restoreErr != nil {
		return stateFixture.restoreErr
	}
	stateFixture.value = data
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
	for _, testCase := range []struct {
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
		t.Run(testCase.name, func(t *testing.T) {
			graphState := NewGraphState()
			stateFixture := &stateFixture{value: "original", restoreErr: testCase.restoreErr}
			graphState.RegisterStateful("saved", stateFixture)
			snapshot := &RunState{Extensions: map[string]json.RawMessage{"middleware:unknown": json.RawMessage(`"ignored"`)}}
			if testCase.raw != nil {
				snapshot.Extensions["middleware:saved"] = testCase.raw
			}
			err := graphState.RestoreExtensions(snapshot)
			if (err != nil) != testCase.wantError {
				t.Fatalf("error=%v want=%v", err, testCase.wantError)
			}
			if stateFixture.value != "original" {
				t.Fatalf("value mutated: %q", stateFixture.value)
			}
		})
	}
}
