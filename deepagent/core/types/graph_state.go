package types

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type GraphLocalState struct{}

func init() {
	schema.RegisterName[*GraphLocalState]("my_empty_state")
}

// RunTimeStateful serializes and restores a component's runtime state.
type RunTimeStateful interface {
	MarshalRuntimeState() string
	UnmarshalRuntimeState(data string) error
}

type GraphStateEntry struct {
	persist  bool
	stateful RunTimeStateful
}

// GraphState holds runtime state entries and their checkpoint store.
type GraphState struct {
	StateHolder map[string]GraphStateEntry
	store       compose.CheckPointStore
}

func StateFromContext(ctx context.Context) *GraphState {
	i := ctx.Value("deep_agent_graph_state")
	if i == nil {
		return nil
	}
	res, ok := i.(*GraphState)
	if !ok {
		return nil
	}
	return res
}

func NewStateContext(ctx context.Context, state *GraphState) context.Context {
	return context.WithValue(ctx, "deep_agent_graph_state", state)
}

func NewGraphState(store compose.CheckPointStore) *GraphState {
	return &GraphState{
		store:       store,
		StateHolder: make(map[string]GraphStateEntry),
	}
}

func (as *GraphState) GetStateful(name string) RunTimeStateful {
	entry, ok := as.StateHolder[name]
	if !ok {
		return nil
	}
	return entry.stateful
}

func (as *GraphState) RegisterStateful(name string, stateful RunTimeStateful) {
	as.registerStatefulWithPersistence(name, stateful, true)
}

func (as *GraphState) RegisterRuntimeOnlyStateful(name string, stateful RunTimeStateful) {
	as.registerStatefulWithPersistence(name, stateful, false)
}

func (as *GraphState) registerStatefulWithPersistence(name string, stateful RunTimeStateful, persist bool) {
	as.StateHolder[name] = GraphStateEntry{
		stateful: stateful,
		persist:  persist,
	}
}

func (as *GraphState) Save(ctx context.Context, graphCheckpointID string) error {
	if as.store == nil {
		return nil
	}
	jsonBytes, err := as.encode()
	if err != nil {
		return err
	}
	key := graphStateKey(graphCheckpointID)
	err = as.store.Set(ctx, key, jsonBytes)
	if err != nil {
		slog.ErrorContext(ctx, "save graph state failed", "error", err)
		return err
	}
	return nil
}

func (as *GraphState) Resume(ctx context.Context, graphCheckpointID string) error {
	if as.store == nil {
		return nil
	}
	key := graphStateKey(graphCheckpointID)
	data, exists, err := as.store.Get(ctx, key)
	if !exists {
		slog.WarnContext(ctx, "graph state checkpoint not found")
		return nil
	}
	if err != nil {
		slog.ErrorContext(ctx, "load graph state failed", "error", err)
		return nil
	}
	return as.decode(data)
}

func graphStateKey(graphCheckpointID string) string {
	return fmt.Sprintf("%s:%s", "deepagent_graph_state_", graphCheckpointID)
}

func (as *GraphState) encode() ([]byte, error) {
	jsonData := make(map[string]string)
	for name, entry := range as.StateHolder {
		if !entry.persist {
			continue
		}
		data := entry.stateful.MarshalRuntimeState()
		jsonData[name] = data
	}
	return sonic.Marshal(jsonData)
}

func (as *GraphState) decode(data []byte) error {
	jsonData := make(map[string]string)
	var err error
	if err = sonic.Unmarshal(data, &jsonData); err != nil {
		return err
	}
	for name, entry := range as.StateHolder {
		if !entry.persist {
			continue
		}
		if id, ok := jsonData[name]; ok {
			if err := entry.stateful.UnmarshalRuntimeState(id); err != nil {
				return err
			}
		}
	}
	return nil
}

// RestoreExtensions keeps the retained public GraphState API backed by the
// single RunState checkpoint. New graph execution does not call Save/Resume.
func (as *GraphState) RestoreExtensions(state *RunState) error {
	for name, entry := range as.StateHolder {
		raw, ok := state.Extensions["middleware:"+name]
		if !ok || !entry.persist {
			continue
		}
		var encoded string
		if err := json.Unmarshal(raw, &encoded); err != nil {
			return err
		}
		if err := entry.stateful.UnmarshalRuntimeState(encoded); err != nil {
			return err
		}
	}
	return nil
}
func (as *GraphState) SnapshotExtensions(state *RunState) error {
	if state.Extensions == nil {
		state.Extensions = make(map[string]json.RawMessage)
	}
	for name, entry := range as.StateHolder {
		if !entry.persist {
			continue
		}
		raw, err := json.Marshal(entry.stateful.MarshalRuntimeState())
		if err != nil {
			return err
		}
		state.Extensions["middleware:"+name] = raw
	}
	return nil
}
