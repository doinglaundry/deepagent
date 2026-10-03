package checkpointer

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/compose"
)

type Envelope struct {
	Version      int
	ThreadID     string
	RunID        string
	GraphVersion string
	EinoSnapshot []byte
}

type Store struct {
	inner        compose.CheckPointStore
	threadID     string
	runID        string
	graphVersion string
}

func New(inner compose.CheckPointStore, threadID, runID, graphVersion string) *Store {
	return &Store{inner: inner, threadID: threadID, runID: runID, graphVersion: graphVersion}
}

func (checkpointStore *Store) Get(ctx context.Context, id string) ([]byte, bool, error) {
	snapshot, exists, err := checkpointStore.readSnapshot(ctx, id)
	if err != nil || !exists {
		return snapshot, exists, err
	}
	if checkpointStore.graphVersion == "core-graph-v1" {
		err := rejectUnknownToolOutcome(snapshot)
		if err != nil {
			return nil, false, err
		}
	}
	return snapshot, true, nil
}

func (checkpointStore *Store) readSnapshot(ctx context.Context, id string) ([]byte, bool, error) {
	raw, exists, err := checkpointStore.inner.Get(ctx, id)
	if err != nil || !exists {
		return nil, exists, err
	}
	var envelope Envelope
	decodeErr := json.Unmarshal(raw, &envelope)
	if decodeErr != nil {
		return nil, false, fmt.Errorf("decode checkpoint envelope: %w", decodeErr)
	}
	if envelope.Version != 1 {
		return nil, false, fmt.Errorf("unsupported checkpoint envelope version %d", envelope.Version)
	}
	if envelope.ThreadID != checkpointStore.threadID || envelope.RunID != checkpointStore.runID {
		return nil, false, fmt.Errorf("checkpoint identity mismatch")
	}
	if envelope.GraphVersion != checkpointStore.graphVersion {
		return nil, false, fmt.Errorf("checkpoint graph version mismatch: %s", envelope.GraphVersion)
	}
	if len(envelope.EinoSnapshot) == 0 {
		return nil, false, fmt.Errorf("empty Eino snapshot")
	}
	if checkpointStore.graphVersion == "core-graph-v1" {
		err := rejectTerminalSnapshot(envelope.EinoSnapshot)
		if err != nil {
			return nil, false, err
		}
	}
	return envelope.EinoSnapshot, true, nil
}

func (checkpointStore *Store) Set(ctx context.Context, id string, snapshot []byte) error {
	if len(snapshot) == 0 {
		return fmt.Errorf("empty Eino snapshot")
	}
	if checkpointStore.graphVersion == "core-graph-v1" {
		var err error
		snapshot, err = markSnapshotBlocked(snapshot)
		if err != nil {
			return err
		}
	}
	return checkpointStore.writeSnapshot(ctx, id, snapshot)
}

// Preserve unknown envelope metadata while replacing only the Eino snapshot.
// Finalize also uses this write, bypassing Set's blocked-phase normalization.
func (checkpointStore *Store) writeSnapshot(ctx context.Context, id string, snapshot []byte) error {
	raw, exists, err := checkpointStore.inner.Get(ctx, id)
	if err != nil {
		return err
	}
	envelope := map[string]json.RawMessage{}
	if exists {
		var identity Envelope
		decodeErr := json.Unmarshal(raw, &identity)
		if decodeErr == nil && identity.Version == 1 && identity.ThreadID == checkpointStore.threadID && identity.RunID == checkpointStore.runID && identity.GraphVersion == checkpointStore.graphVersion {
			err = json.Unmarshal(raw, &envelope)
			if err != nil {
				return err
			}
		}
	}
	// A forced fresh execution can reuse an old key. Only matching envelopes
	// carry metadata forward; stale identities or invalid bytes are replaced.
	if len(envelope) == 0 {
		raw, err = json.Marshal(Envelope{Version: 1, ThreadID: checkpointStore.threadID, RunID: checkpointStore.runID, GraphVersion: checkpointStore.graphVersion})
		if err != nil {
			return err
		}
		err = json.Unmarshal(raw, &envelope)
		if err != nil {
			return err
		}
	}
	envelope["EinoSnapshot"], err = json.Marshal(snapshot)
	if err != nil {
		return err
	}
	raw, err = json.Marshal(envelope)
	if err != nil {
		return err
	}
	return checkpointStore.inner.Set(ctx, id, raw)
}

var _ compose.CheckPointStore = (*Store)(nil)

// checkpointValue contains Eino's recursive interrupt-address maps.
type checkpointValue struct {
	MapValues   map[string]*checkpointValue `json:",omitempty"`
	SliceValues []*checkpointValue          `json:",omitempty"`
}
