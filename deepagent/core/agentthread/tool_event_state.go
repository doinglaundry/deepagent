package agentthread

import (
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/types"
	"github.com/bytedance/sonic"
	"strings"
	"sync"
	"time"
)

type toolEventState struct {
	name            string
	callID          string
	toolStartTime   time.Time
	argumentsInJSON string
	startEmitted    bool
	streamSeen      bool
	endEmitted      bool
	result          strings.Builder
	mu              sync.Mutex
}
type toolEventSnapshot struct {
	name            string
	callID          string
	toolStartTime   time.Time
	argumentsInJSON string
}

func toolEventKey(callID, name string) string {
	if callID != "" {
		return callID
	}
	return name
}
func (s *toolEventState) markStartEmitted(toolStartTime time.Time, argumentsInJSON string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.startEmitted {
		return false
	}
	s.toolStartTime = toolStartTime
	s.argumentsInJSON = argumentsInJSON
	s.startEmitted = true
	return true
}
func (s *toolEventState) markStreamStarted() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streamSeen = true
}
func (s *toolEventState) isPendingStreamBinding() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.streamSeen && !s.endEmitted
}
func (s *toolEventState) markStreamSeen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streamSeen
}
func (s *toolEventState) appendResult(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.result.WriteString(text)
}
func (s *toolEventState) setCallID(callID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.callID == "" {
		s.callID = callID
	}
}
func (s *toolEventState) snapshotResult() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result.String()
}
func (s *toolEventState) snapshot() toolEventSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return toolEventSnapshot{name: s.name, callID: s.callID, toolStartTime: s.toolStartTime, argumentsInJSON: s.argumentsInJSON}
}
func (s *toolEventState) markEndEmitted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.endEmitted {
		return false
	}
	s.endEmitted = true
	return true
}

// toolEventMiddleware persists tool callback dedupe state through graph
// interrupt/resume.
type toolEventMiddleware struct {
	middleware.BaseMiddleware
	store *toolEventStore
}

func newToolEventMiddleware() *toolEventMiddleware {
	return &toolEventMiddleware{store: newToolEventStore()}
}
func (m *toolEventMiddleware) Name() string                             { return "__tool_events" }
func (m *toolEventMiddleware) BuildStateHandler() types.RunTimeStateful { return m.store }

type toolEventStore struct{ m sync.Map }

func newToolEventStore() *toolEventStore { return &toolEventStore{} }
func (s *toolEventStore) load(key string) (*toolEventState, bool) {
	v, ok := s.m.Load(key)
	if !ok {
		return nil, false
	}
	return v.(*toolEventState), true
}
func (s *toolEventStore) loadOrStore(key string, state *toolEventState) *toolEventState {
	actual, _ := s.m.LoadOrStore(key, state)
	return actual.(*toolEventState)
}
func (s *toolEventStore) rangeStates(fn func(key string, state *toolEventState) bool) {
	s.m.Range(func(k, v any) bool {
		state, ok := v.(*toolEventState)
		if !ok {
			return true
		}
		return fn(k.(string), state)
	})
}

type toolEventStatePersist struct {
	Name            string    `json:"name"`
	CallID          string    `json:"call_id"`
	ToolStartTime   time.Time `json:"tool_start_time"`
	ArgumentsInJSON string    `json:"arguments_in_json"`
	StartEmitted    bool      `json:"start_emitted"`
	EndEmitted      bool      `json:"end_emitted"`
}

func (s *toolEventStore) MarshalRuntimeState() string {
	snapshot := map[string]*toolEventStatePersist{}
	s.m.Range(func(k, v any) bool {
		state, ok := v.(*toolEventState)
		if !ok {
			return true
		}
		state.mu.Lock()
		snapshot[k.(string)] = &toolEventStatePersist{
			Name:            state.name,
			CallID:          state.callID,
			ToolStartTime:   state.toolStartTime,
			ArgumentsInJSON: state.argumentsInJSON,
			StartEmitted:    state.startEmitted,
			EndEmitted:      state.endEmitted,
		}
		state.mu.Unlock()
		return true
	})
	data, _ := sonic.MarshalString(snapshot)
	return data
}
func (s *toolEventStore) UnmarshalRuntimeState(data string) error {
	snapshot := map[string]*toolEventStatePersist{}
	if err := sonic.UnmarshalString(data, &snapshot); err != nil {
		return err
	}
	for key, p := range snapshot {
		s.m.Store(key, &toolEventState{
			name:            p.Name,
			callID:          p.CallID,
			toolStartTime:   p.ToolStartTime,
			argumentsInJSON: p.ArgumentsInJSON,
			startEmitted:    p.StartEmitted,
			endEmitted:      p.EndEmitted,
		})
	}
	return nil
}
