package checkpointer

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/schema"
)

// This wire decoder is pinned to Eino v0.9.0-alpha.17's historical JSON codec.
// It converts data only; it never executes the old graph or replays a tool.
type legacyValue struct {
	Type        json.RawMessage         `json:",omitempty"`
	JSONValue   json.RawMessage         `json:",omitempty"`
	MapValues   map[string]*legacyValue `json:",omitempty"`
	SliceValues []*legacyValue          `json:",omitempty"`
}

func (s *Store) migrateLegacy(ctx context.Context, id string, raw []byte) ([]byte, error) {
	if s.graphVersion != "core-graph-v1" {
		return nil, fmt.Errorf("legacy migration does not support graph version %q", s.graphVersion)
	}
	var cp legacyValue
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil, err
	}
	var typ struct{ StructType string }
	if err := json.Unmarshal(cp.Type, &typ); err != nil || typ.StructType != "_eino_checkpoint" {
		return nil, fmt.Errorf("unsupported legacy checkpoint codec")
	}
	fields := cp.MapValues
	state := fields["State"]
	if state == nil {
		return nil, fmt.Errorf("legacy checkpoint state missing")
	}
	if err := json.Unmarshal(state.Type, &typ); err != nil || typ.StructType != "my_empty_state" {
		return nil, fmt.Errorf("unsupported legacy graph state")
	}
	for _, name := range []string{"SubGraphs", "RerunNodes", "SkipPreHandler"} {
		if value := fields[name]; value != nil && (len(value.MapValues) > 0 || len(value.SliceValues) > 0 || len(value.JSONValue) > 0) {
			return nil, fmt.Errorf("legacy checkpoint %s requires explicit migration", name)
		}
	}
	inputs := fields["Inputs"]
	if inputs == nil || len(inputs.MapValues) != 1 {
		return nil, fmt.Errorf("legacy checkpoint requires one suspended node")
	}
	nodeKey, nextKey := `"model"`, `"prepare"`
	inputType := reflect.TypeOf([]*schema.Message{})
	if inputs.MapValues[`"tools"`] != nil {
		nodeKey, nextKey, inputType = `"tools"`, `"tools"`, reflect.TypeOf(schema.Message{})
	}
	if inputs.MapValues[nodeKey] == nil {
		return nil, fmt.Errorf("unsupported legacy node")
	}
	channels := fields["Channels"]
	if channels == nil {
		return nil, fmt.Errorf("legacy checkpoint channels missing")
	}
	for _, channel := range channels.MapValues {
		if channel != nil {
			value := channel.MapValues["Values"]
			if value != nil && (len(value.MapValues) > 0 || len(value.SliceValues) > 0 || len(value.JSONValue) > 0) {
				return nil, fmt.Errorf("legacy checkpoint contains pending channel output")
			}
		}
	}
	plain, err := legacyJSON(inputs.MapValues[nodeKey], inputType)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(plain)
	if err != nil {
		return nil, err
	}
	converted := types.RunState{Version: 1, ThreadID: s.threadID, RunID: s.runID, Phase: types.PhasePreparing, Extensions: map[string]json.RawMessage{}}
	if nodeKey == `"tools"` {
		var message schema.Message
		if err := json.Unmarshal(encoded, &message); err != nil {
			return nil, err
		}
		if message.Role != schema.Assistant || len(message.ToolCalls) == 0 {
			return nil, fmt.Errorf("legacy tools input has no assistant calls")
		}
		seen := map[string]bool{}
		for i, call := range message.ToolCalls {
			if call.ID == "" || call.Function.Name == "" || seen[call.ID] {
				return nil, fmt.Errorf("invalid legacy tool identity")
			}
			seen[call.ID] = true
			converted.Calls = append(converted.Calls, types.ToolCallState{Call: types.ToolCall{ID: call.ID, Index: i, Name: call.Function.Name, Arguments: call.Function.Arguments}, Status: types.CallPending})
		}
		converted.Phase = types.PhaseTools
		converted.Extensions["legacy_tools_message"] = encoded
	} else {
		var messages []*schema.Message
		if err := json.Unmarshal(encoded, &messages); err != nil {
			return nil, err
		}
		if len(messages) == 0 {
			return nil, fmt.Errorf("legacy model input is empty")
		}
		for _, message := range messages {
			if message == nil || message.Role != schema.User {
				return nil, fmt.Errorf("legacy model boundary has non-user inputs; history reconciliation required")
			}
			converted.Consumed = append(converted.Consumed, types.Input{Message: message})
		}
	}
	sidecar, exists, err := s.inner.Get(ctx, "deepagent_graph_state_:"+id)
	if err != nil {
		return nil, fmt.Errorf("load legacy middleware state: %w", err)
	}
	if exists {
		var states map[string]string
		if err := json.Unmarshal(sidecar, &states); err != nil {
			return nil, fmt.Errorf("decode legacy middleware state: %w", err)
		}
		for name, value := range states {
			if name == "simple_context_manager" {
				var history []*schema.Message
				if err := json.Unmarshal([]byte(value), &history); err != nil {
					return nil, fmt.Errorf("decode legacy context history: %w", err)
				}
				// Use the same reconciliation boundary as legacy engine history.
				// Restoring a middleware-owned copy would leave Conversation empty.
				if len(history) > 0 {
					converted.Extensions["legacy_engine_history"], err = json.Marshal(history)
					if err != nil {
						return nil, err
					}
				}
				continue
			}
			if name == "deepagents.max_model_calls" {
				var budget struct {
					Consumed int `json:"consumed"`
				}
				if err := json.Unmarshal([]byte(value), &budget); err != nil || budget.Consumed < 0 {
					return nil, fmt.Errorf("invalid legacy model budget")
				}
				converted.ModelCalls = budget.Consumed
				continue
			}
			converted.Extensions["middleware:"+name], err = json.Marshal(value)
			if err != nil {
				return nil, err
			}
		}
	}
	stateJSON, err := json.Marshal(converted)
	if err != nil {
		return nil, err
	}
	newState := &legacyValue{Type: json.RawMessage(`{"PointerNum":1,"SimpleType":"deepagent_run_state_v1"}`), JSONValue: stateJSON}
	fields["State"] = newState
	fields["Inputs"] = &legacyValue{MapValues: map[string]*legacyValue{nextKey: newState}}
	// Root interrupt identities stay stable; only the renamed runnable address
	// changes. The remaining address segments must not be rewritten.
	if addresses := fields["InterruptID2Addr"]; addresses != nil {
		for _, address := range addresses.MapValues {
			if address == nil || len(address.SliceValues) == 0 {
				return nil, fmt.Errorf("invalid legacy interrupt address")
			}
			root := address.SliceValues[0]
			if root == nil || root.MapValues["ID"] == nil {
				return nil, fmt.Errorf("invalid legacy root address")
			}
			var name string
			if err := json.Unmarshal(root.MapValues["ID"].JSONValue, &name); err != nil || name != "deep_agent" {
				return nil, fmt.Errorf("unsupported legacy runnable address")
			}
			root.MapValues["ID"].JSONValue = json.RawMessage(`"deepagent"`)
		}
	}
	return json.Marshal(cp)
}

func legacyJSON(value *legacyValue, target reflect.Type) (any, error) {
	if value == nil {
		return nil, nil
	}
	if len(value.JSONValue) > 0 {
		return value.JSONValue, nil
	}
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if target.Kind() == reflect.Slice || value.SliceValues != nil {
		elementType := reflect.TypeOf((*any)(nil)).Elem()
		if target.Kind() == reflect.Slice {
			elementType = target.Elem()
		}
		out := make([]any, len(value.SliceValues))
		for i, item := range value.SliceValues {
			decoded, err := legacyJSON(item, elementType)
			if err != nil {
				return nil, err
			}
			out[i] = decoded
		}
		return out, nil
	}
	out := map[string]any{}
	for key, item := range value.MapValues {
		if item == nil {
			continue
		}
		fieldType := reflect.TypeOf((*any)(nil)).Elem()
		jsonKey := key
		if target.Kind() == reflect.Struct {
			field, ok := target.FieldByName(key)
			if !ok {
				return nil, fmt.Errorf("unknown legacy field %s.%s", target, key)
			}
			fieldType = field.Type
			if tag := strings.Split(field.Tag.Get("json"), ",")[0]; tag != "" {
				jsonKey = tag
			}
		} else {
			if unquoted, err := strconv.Unquote(key); err == nil {
				jsonKey = unquoted
			}
			if target.Kind() == reflect.Map {
				fieldType = target.Elem()
			}
		}
		decoded, err := legacyJSON(item, fieldType)
		if err != nil {
			return nil, err
		}
		out[jsonKey] = decoded
	}
	return out, nil
}
