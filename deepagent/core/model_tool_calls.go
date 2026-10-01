package deepagents

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

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
			{
				found, ok := b.byProvider[*delta.Index]
				if ok {
					index = found
				}
			}
		}
		if delta.ID != "" {
			{
				found, ok := b.byID[delta.ID]
				if ok {
					if index >= 0 && index != found {
						return nil, fmt.Errorf("conflicting tool ID and provider index")
					}
					index = found
				}
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
			{
				repaired, err := repairToolArguments(call.Arguments)
				if err == nil && json.Valid([]byte(repaired)) {
					call.Arguments = repaired
				}
			}
		}
		index := call.Index
		result = append(result, schema.ToolCall{ID: call.ID, Index: &index, Type: "function", Function: schema.FunctionCall{Name: call.Name, Arguments: call.Arguments}})
	}
	return result, nil
}

// repairToolArguments strips an enclosing Markdown code fence and trailing commas only.
// It never invents missing field names, quotes, values, or delimiters.
func repairToolArguments(input string) (string, error) {
	s := strings.TrimSpace(input)
	if strings.HasPrefix(s, "```") && strings.HasSuffix(s, "```") {
		i := strings.IndexByte(s, '\n')
		if i >= 0 {
			s = strings.TrimSpace(s[i+1 : len(s)-3])
		}
	}
	var b strings.Builder
	quoted, escaped := false, false
	for i, r := range s {
		if quoted {
			b.WriteRune(r)
			if escaped {
				escaped = false
			} else if r == '\\' {
				escaped = true
			} else if r == '"' {
				quoted = false
			}
			continue
		}
		if r == '"' {
			quoted = true
			b.WriteRune(r)
			continue
		}
		if r == ',' {
			j := i + 1
			for j < len(s) && unicode.IsSpace(rune(s[j])) {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				continue
			}
		}
		b.WriteRune(r)
	}
	out := b.String()
	if !json.Valid([]byte(out)) {
		return "", errors.New("invalid JSON arguments; only code fences and trailing commas can be repaired")
	}
	return out, nil
}
