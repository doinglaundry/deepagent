package checkpointer

import (
	"encoding/json"
	"fmt"
	"strconv"

	"eino-cli/deepagent/core/types"
	"eino-cli/deepagent/protocol"
	"github.com/cloudwego/eino/schema"
)

// engineState is the persisted format of the retired distributed engine.
// It is decoded only for migration, never used to execute an agent.
type engineState struct {
	ThreadID         string            `json:"thread_id"`
	RunID            string            `json:"run_id"`
	Namespace        string            `json:"namespace"`
	CheckpointID     string            `json:"checkpoint_id"`
	Messages         []*schema.Message `json:"messages"`
	MessageIDs       []string          `json:"message_ids"`
	Calls            []schema.ToolCall `json:"calls"`
	ToolIndex        int               `json:"tool_index"`
	ModelCalls       int               `json:"model_calls"`
	Steps            int               `json:"steps"`
	Block            *protocol.Block   `json:"block"`
	Pending          []protocol.Input  `json:"pending"`
	Resume           *protocol.Resume  `json:"resume"`
	TotalTokens      int64             `json:"total_tokens"`
	PromptTokens     int64             `json:"prompt_tokens"`
	CompletionTokens int64             `json:"completion_tokens"`
}

func (s *Store) migrateEngine(id string, raw []byte) ([]byte, error) {
	var old engineState
	if err := json.Unmarshal(raw, &old); err != nil {
		return nil, err
	}
	if s.graphVersion != "core-graph-v1" || old.ThreadID != s.threadID || old.RunID != s.runID || old.Namespace == "" || old.CheckpointID != id {
		return nil, fmt.Errorf("legacy engine checkpoint identity mismatch")
	}
	b := old.Block
	if b == nil || b.RunID != old.RunID || b.CheckpointID != id || b.InterruptID == "" || old.Resume != nil || old.ToolIndex < 0 || old.ToolIndex >= len(old.Calls) {
		return nil, fmt.Errorf("legacy engine checkpoint is not a suspended tool boundary")
	}
	if b.Kind != "approval" && b.Kind != "clarification" {
		return nil, fmt.Errorf("unsupported legacy engine block kind %q", b.Kind)
	}
	if old.ModelCalls < 0 || old.Steps < 0 || old.TotalTokens < 0 || old.PromptTokens < 0 || old.CompletionTokens < 0 {
		return nil, fmt.Errorf("invalid legacy engine counters")
	}
	blocked := old.Calls[old.ToolIndex]
	if b.ToolName != blocked.Function.Name || b.Arguments != blocked.Function.Arguments || (b.Kind == "clarification" && b.ToolName != "ask_user") {
		return nil, fmt.Errorf("legacy block does not match suspended call")
	}
	// A tool block is saved after the assistant request and exactly ToolIndex
	// results. Reject other tails rather than guessing which effects completed.
	assistantIndex := len(old.Messages) - old.ToolIndex - 1
	if assistantIndex < 0 {
		return nil, fmt.Errorf("legacy engine history is incomplete")
	}
	for _, message := range old.Messages {
		if message == nil {
			return nil, fmt.Errorf("nil legacy history message")
		}
	}
	assistant := old.Messages[assistantIndex]
	if assistant.Role != schema.Assistant || len(assistant.ToolCalls) != len(old.Calls) {
		return nil, fmt.Errorf("legacy assistant call boundary mismatch")
	}
	state := types.RunState{Version: 1, ThreadID: old.ThreadID, RunID: old.RunID, AgentName: "deepagent", Phase: types.PhaseBlocked, ModelCalls: old.ModelCalls, GraphSteps: old.Steps, Extensions: map[string]json.RawMessage{}}
	seen := map[string]bool{}
	for i, call := range old.Calls {
		if call.ID == "" || call.Function.Name == "" || seen[call.ID] || assistant.ToolCalls[i].ID != call.ID || assistant.ToolCalls[i].Function != call.Function {
			return nil, fmt.Errorf("invalid legacy call identity")
		}
		seen[call.ID] = true
		entry := types.ToolCallState{Call: types.ToolCall{ID: call.ID, Index: i, Name: call.Function.Name, Arguments: call.Function.Arguments}, Status: types.CallPending}
		if i < old.ToolIndex {
			result := old.Messages[assistantIndex+1+i]
			if result.Role != schema.Tool || result.ToolCallID != call.ID {
				return nil, fmt.Errorf("legacy completed tool result mismatch")
			}
			entry.Status = types.CallCompleted
			entry.Result = &types.ToolResult{CallID: call.ID, Content: result.Content}
		} else if i == old.ToolIndex {
			entry.Status = types.CallBlocked
		}
		state.Calls = append(state.Calls, entry)
	}
	// Preserve consumption IDs even when compaction removed the source message.
	for _, id := range old.MessageIDs {
		message := schema.UserMessage("")
		for _, candidate := range old.Messages {
			if candidate.Role == schema.User && candidate.Extra["deepagent_input_id"] == id {
				message = candidate
				break
			}
		}
		copied := *message
		copied.Extra = make(map[string]any, len(message.Extra)+1)
		for key, value := range message.Extra {
			copied.Extra[key] = value
		}
		copied.Extra["message_id"] = id
		state.Consumed = append(state.Consumed, types.Input{Message: &copied})
	}
	state.Extensions["prepared_inputs"], _ = json.Marshal(len(state.Consumed))
	identities := map[string]*schema.Message{}
	for _, input := range state.Consumed {
		if id, ok := input.Message.Extra["message_id"].(string); ok {
			identities[id] = input.Message
		}
	}
	for _, input := range old.Pending {
		message, err := engineInput(input, old.ThreadID)
		if err != nil {
			return nil, err
		}
		if prior := identities[input.ID]; prior != nil {
			a, _ := json.Marshal(prior)
			b, _ := json.Marshal(message)
			if string(a) != string(b) {
				return nil, fmt.Errorf("legacy pending input identity reused with different content")
			}
			continue
		}
		identities[input.ID] = message
		state.Consumed = append(state.Consumed, types.Input{Message: message})
		state.Extensions["legacy_pending_inputs"] = json.RawMessage(`true`)
	}

	state.Extensions["legacy_engine_history"], _ = json.Marshal(old.Messages)
	state.Extensions["legacy_persisted_tool_results"], _ = json.Marshal(old.ToolIndex)
	state.Usage = types.Usage{PromptTokens: old.PromptTokens, CompletionTokens: old.CompletionTokens, TotalTokens: old.TotalTokens}
	state.Pending = []types.Interrupt{{InterruptID: b.InterruptID, CallID: blocked.ID, CheckpointID: id, Kind: b.Kind}}
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	value := &legacyValue{Type: json.RawMessage(`{"PointerNum":1,"SimpleType":"deepagent_run_state_v1"}`), JSONValue: encoded}
	scalar := func(v string) *legacyValue { raw, _ := json.Marshal(v); return &legacyValue{JSONValue: raw} }
	segment := func(id, kind string) *legacyValue {
		return &legacyValue{MapValues: map[string]*legacyValue{"ID": scalar(id), "Type": scalar(kind)}}
	}
	root := segment("deepagent", "runnable")
	rootID := strconv.Quote("legacy-root/" + b.InterruptID)
	interruptID := strconv.Quote(b.InterruptID)
	channels := map[string]*legacyValue{}
	for _, node := range []string{"prepare", "model", "tools", "continue", "finish", "end"} {
		channels[strconv.Quote(node)] = &legacyValue{Type: json.RawMessage(`{"PointerNum":1,"StructType":"_eino_pregel_channel"}`), MapValues: map[string]*legacyValue{"Values": {}}}
	}
	cp := legacyValue{Type: json.RawMessage(`{"PointerNum":1,"StructType":"_eino_checkpoint"}`), MapValues: map[string]*legacyValue{
		"State": value, "Inputs": {MapValues: map[string]*legacyValue{`"tools"`: value}}, "Channels": {MapValues: channels},
		"RerunNodes": {SliceValues: []*legacyValue{scalar("tools")}}, "SkipPreHandler": {}, "SubGraphs": {},
		"InterruptID2State": {MapValues: map[string]*legacyValue{rootID: nil, interruptID: nil}},
		"InterruptID2Addr":  {MapValues: map[string]*legacyValue{rootID: {SliceValues: []*legacyValue{root}}, interruptID: {SliceValues: []*legacyValue{root, segment("tools", "node")}}}},
	}}
	return json.Marshal(cp)
}
