package graph

import (
	"context"
	"eino-cli/deepagent/core/types"
	"encoding/json"
	"strings"
	"sync"
)

// A child writes its Eino envelope here during execution. The caller embeds
// the bytes in the parent RunState before propagating interruption, so the
// parent's Eino checkpoint is the only external persistence operation.
type childCheckpointStore struct {
	mu   sync.Mutex
	data []byte
}

func (s *childCheckpointStore) Get(ctx context.Context, _ string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.data...), len(s.data) > 0, nil
}
func (s *childCheckpointStore) Set(ctx context.Context, _ string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = append([]byte(nil), data...)
	return nil
}

type toolCallIDKey struct{}

type toolExecutorKey struct{}

func (e *toolExecutor) childCheckpoint(callID string) *childCheckpointStore {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.childCheckpoints == nil {
		e.childCheckpoints = map[string]*childCheckpointStore{}
	}
	if e.childCheckpoints[callID] == nil {
		e.childCheckpoints[callID] = &childCheckpointStore{}
	}
	return e.childCheckpoints[callID]
}

func (e *toolExecutor) restoreChildCheckpoints(state *types.RunState) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.childCheckpoints = map[string]*childCheckpointStore{}
	for key, raw := range state.Extensions {
		if id, ok := strings.CutPrefix(key, "child_checkpoint/"); ok {
			e.childCheckpoints[id] = &childCheckpointStore{data: append([]byte(nil), raw...)}
		}
	}
}

// Only the graph node writes RunState; concurrent child executions write their
// own locked buffers. The executor owns both eager and ordinary task buffers.
func (e *toolExecutor) snapshotChildCheckpoints(state *types.RunState) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, checkpoint := range e.childCheckpoints {
		checkpoint.mu.Lock()
		raw := append([]byte(nil), checkpoint.data...)
		checkpoint.mu.Unlock()
		key := "child_checkpoint/" + id
		if len(raw) == 0 {
			delete(state.Extensions, key)
			continue
		}
		if state.Extensions == nil {
			state.Extensions = map[string]json.RawMessage{}
		}
		state.Extensions[key] = raw
	}
}
