package graph

import (
	"encoding/json"
	"fmt"
	"strings"

	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/schema"
)

// toolCallBuffer lives for exactly one model stream. Provider indexes are map
// keys; the index used by the executor is always the contiguous arrival order.
type toolCallBuffer struct {
	calls      []types.ToolCall
	byProvider map[int]int
	byID       map[string]int
	started    map[string]bool
}

func (b *toolCallBuffer) add(deltas []schema.ToolCall) ([]types.ToolCall, error) {
	if b.byProvider == nil {
		b.byProvider = map[int]int{}
		b.byID = map[string]int{}
		b.started = map[string]bool{}
	}
	var ready []types.ToolCall
	for _, delta := range deltas {
		index := -1
		if delta.Index != nil {
			if found, ok := b.byProvider[*delta.Index]; ok {
				index = found
			}
		}
		if delta.ID != "" {
			if found, ok := b.byID[delta.ID]; ok {
				if index >= 0 && index != found {
					return nil, fmt.Errorf("conflicting tool ID and provider index")
				}
				index = found
			}
		}
		if index < 0 {
			if delta.Index == nil && delta.ID == "" {
				if len(b.calls) != 1 {
					return nil, fmt.Errorf("ambiguous tool fragment without identity")
				}
				index = 0
			} else {
				index = len(b.calls)
				b.calls = append(b.calls, types.ToolCall{Index: index})
			}
		}
		call := &b.calls[index]
		if delta.Index != nil {
			b.byProvider[*delta.Index] = index
		}
		if delta.ID != "" {
			if call.ID != "" && call.ID != delta.ID {
				return nil, fmt.Errorf("tool call ID changed")
			}
			call.ID = delta.ID
			b.byID[delta.ID] = index
		}
		if delta.Function.Name != "" {
			if call.Name == "" {
				call.Name = delta.Function.Name
			} else if call.Name != delta.Function.Name {
				call.Name += delta.Function.Name
			}
		}
		if b.started[call.ID] && strings.TrimSpace(delta.Function.Arguments) != "" {
			return nil, fmt.Errorf("tool arguments changed after eager execution")
		}
		call.Arguments += delta.Function.Arguments
		if call.ID != "" && call.Name != "" && json.Valid([]byte(call.Arguments)) && !b.started[call.ID] {
			ready = append(ready, *call)
		}
	}
	return ready, nil
}
func (b *toolCallBuffer) finish() ([]schema.ToolCall, error) {
	result := make([]schema.ToolCall, 0, len(b.calls))
	for _, call := range b.calls {
		if call.ID == "" || call.Name == "" {
			return nil, fmt.Errorf("incomplete tool call identity")
		}
		// Only repair after EOF: a partial fragment may still change. Preserve
		// unrepairable arguments so the tool can report its validation error.
		if !json.Valid([]byte(call.Arguments)) {
			if repaired, err := repairToolArguments(call.Arguments); err == nil && json.Valid([]byte(repaired)) {
				call.Arguments = repaired
			}
		}
		index := call.Index
		result = append(result, schema.ToolCall{ID: call.ID, Index: &index, Type: "function", Function: schema.FunctionCall{Name: call.Name, Arguments: call.Arguments}})
	}
	return result, nil
}
