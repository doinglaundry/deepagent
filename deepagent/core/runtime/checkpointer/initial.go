package checkpointer

import (
	"encoding/json"
	"fmt"
	"reflect"

	"eino-cli/deepagent/core/types"
)

// The first canonical graph generated an empty local state until prepare ran.
// Its pre-prepare checkpoint still contains the complete original graph input.
// Only that unexecuted boundary is eligible for this explicit migration.
func (s *Store) migrateEmptyInitial(snapshot []byte) ([]byte, bool, error) {
	var cp legacyValue
	if err := json.Unmarshal(snapshot, &cp); err != nil {
		return nil, false, err
	}
	state := cp.MapValues["State"]
	if state == nil {
		return snapshot, false, nil
	}
	var typ struct{ SimpleType string }
	if json.Unmarshal(state.Type, &typ) != nil || typ.SimpleType != "deepagent_run_state_v1" {
		return snapshot, false, nil
	}
	var empty types.RunState
	if err := json.Unmarshal(state.JSONValue, &empty); err != nil {
		return nil, false, err
	}
	if empty.Version != 0 {
		return snapshot, false, nil
	}
	if empty.Phase != "" && empty.Phase != types.PhaseBlocked {
		return nil, false, fmt.Errorf("unexpected empty checkpoint phase %q", empty.Phase)
	}
	empty.Phase = ""
	if !reflect.DeepEqual(empty, types.RunState{}) {
		return nil, false, fmt.Errorf("version 0 checkpoint contains unexpected state")
	}
	inputs := cp.MapValues["Inputs"]
	if inputs == nil || len(inputs.MapValues) != 1 || inputs.MapValues[`"prepare"`] == nil {
		return nil, false, fmt.Errorf("empty checkpoint is not before prepare")
	}
	for _, key := range []string{"RerunNodes", "SubGraphs", "SkipPreHandler"} {
		v := cp.MapValues[key]
		if v != nil && (len(v.MapValues) > 0 || len(v.SliceValues) > 0) {
			return nil, false, fmt.Errorf("empty checkpoint has executed graph work: %s", key)
		}
	}
	if channels := cp.MapValues["Channels"]; channels != nil {
		for _, channel := range channels.MapValues {
			if channel == nil {
				continue
			}
			if values := channel.MapValues["Values"]; values != nil && (len(values.MapValues) > 0 || len(values.SliceValues) > 0 || len(values.JSONValue) > 0) {
				return nil, false, fmt.Errorf("empty checkpoint has pending channel output")
			}
		}
	}
	input := inputs.MapValues[`"prepare"`]
	typ.SimpleType = ""
	if json.Unmarshal(input.Type, &typ) != nil || typ.SimpleType != "deepagent_run_state_v1" {
		return nil, false, fmt.Errorf("invalid initial checkpoint input type")
	}
	var initial types.RunState
	if err := json.Unmarshal(input.JSONValue, &initial); err != nil {
		return nil, false, err
	}
	if initial.Version != 1 || initial.ThreadID != s.threadID || initial.RunID != s.runID || initial.Phase != types.PhasePreparing || initial.GraphSteps != 0 || initial.ModelCalls != 0 || len(initial.Calls) != 0 || len(initial.Pending) != 0 {
		return nil, false, fmt.Errorf("initial checkpoint input is not an unexecuted matching run")
	}
	// Copy raw JSON, not the decoded value: retain arbitrary metadata exactly.
	state.JSONValue = append(json.RawMessage(nil), input.JSONValue...)
	raw, err := json.Marshal(cp)
	if err != nil {
		return nil, false, err
	}
	raw, err = blockedSnapshot(raw)
	return raw, true, err
}
