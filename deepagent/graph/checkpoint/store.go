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

func (s *Store) Get(ctx context.Context, id string) ([]byte, bool, error) {
	snapshot, exists, err := s.get(ctx, id)
	if err != nil || !exists {
		return snapshot, exists, err
	}
	if s.graphVersion == "core-graph-v1" {
		err := rejectUnknownToolOutcome(snapshot)
		if err != nil {
			return nil, false, err
		}
	}
	return snapshot, true, nil
}

func (s *Store) get(ctx context.Context, id string) ([]byte, bool, error) {
	raw, exists, err := s.inner.Get(ctx, id)
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
	if envelope.ThreadID != s.threadID || envelope.RunID != s.runID {
		return nil, false, fmt.Errorf("checkpoint identity mismatch")
	}
	if envelope.GraphVersion != s.graphVersion {
		return nil, false, fmt.Errorf("checkpoint graph version mismatch: %s", envelope.GraphVersion)
	}
	if len(envelope.EinoSnapshot) == 0 {
		return nil, false, fmt.Errorf("empty Eino snapshot")
	}
	if s.graphVersion == "core-graph-v1" {
		err := rejectTerminalSnapshot(envelope.EinoSnapshot)
		if err != nil {
			return nil, false, err
		}
	}
	return envelope.EinoSnapshot, true, nil
}

func (s *Store) Set(ctx context.Context, id string, snapshot []byte) error {
	if len(snapshot) == 0 {
		return fmt.Errorf("empty Eino snapshot")
	}
	if s.graphVersion == "core-graph-v1" {
		var err error
		snapshot, err = blockedSnapshot(snapshot)
		if err != nil {
			return err
		}
	}
	return s.writeSnapshot(ctx, id, snapshot)
}

// Preserve unknown envelope metadata while replacing only the Eino snapshot.
// Finalize also uses this write, bypassing Set's blocked-phase normalization.
func (s *Store) writeSnapshot(ctx context.Context, id string, snapshot []byte) error {
	raw, exists, err := s.inner.Get(ctx, id)
	if err != nil {
		return err
	}
	envelope := map[string]json.RawMessage{}
	if exists {
		var identity Envelope
		decodeErr := json.Unmarshal(raw, &identity)
		if decodeErr == nil && identity.Version == 1 && identity.ThreadID == s.threadID && identity.RunID == s.runID && identity.GraphVersion == s.graphVersion {
			err = json.Unmarshal(raw, &envelope)
			if err != nil {
				return err
			}
		}
	}
	// A forced fresh execution can reuse an old key. Only matching envelopes
	// carry metadata forward; stale identities or invalid bytes are replaced.
	if len(envelope) == 0 {
		raw, err = json.Marshal(Envelope{Version: 1, ThreadID: s.threadID, RunID: s.runID, GraphVersion: s.graphVersion})
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
	return s.inner.Set(ctx, id, raw)
}

var _ compose.CheckPointStore = (*Store)(nil)

// checkpointValue contains Eino's recursive interrupt-address maps.
type checkpointValue struct {
	MapValues   map[string]*checkpointValue `json:",omitempty"`
	SliceValues []*checkpointValue          `json:",omitempty"`
}
