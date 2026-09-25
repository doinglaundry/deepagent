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
		if err := rejectUnknownToolOutcome(snapshot); err != nil {
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
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, false, fmt.Errorf("decode checkpoint envelope: %w", err)
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
		if err := rejectTerminalSnapshot(envelope.EinoSnapshot); err != nil {
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
	raw, err := json.Marshal(Envelope{Version: 1, ThreadID: s.threadID, RunID: s.runID, GraphVersion: s.graphVersion, EinoSnapshot: snapshot})
	if err != nil {
		return err
	}
	return s.inner.Set(ctx, id, raw)
}

var _ compose.CheckPointStore = (*Store)(nil)
