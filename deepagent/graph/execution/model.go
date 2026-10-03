package execution

import (
	"context"
	"eino-cli/deepagent/graph/middleware"
	"eino-cli/deepagent/graph/types"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"io"
	"strings"
	"unicode"
)

var ErrExceedMaxModelCalls = errors.New("exceeds max model calls")

func (a *Graph) callModel(ctx context.Context, s *types.RunState) (*types.RunState, error) {
	if a.cfg.MaxModelCalls > 0 && s.ModelCalls >= a.cfg.MaxModelCalls {
		return nil, fmt.Errorf("%w: maximum model calls exceeded: %d", ErrExceedMaxModelCalls, a.cfg.MaxModelCalls)
	}
	if hasPendingInputs(s) {
		_, err := a.prepare(ctx, s)
		if err != nil {
			return nil, err
		}
	}
	// prepare handles new user input; tool results can independently cross the
	// context limit before the next sampling boundary in the same graph loop.
	if s.Phase == types.PhaseTools {
		err := a.compactContext(ctx, s)
		if err != nil {
			return nil, err
		}
	}
	s.Phase = types.PhaseModeling
	s.ModelCalls++
	prompts := append([]*schema.Message(nil), a.cfg.Prompts...)
	for _, mw := range a.middlewares {
		built, err := mw.BuildPrompt(ctx)
		if err != nil {
			return nil, err
		}
		prompts = append(prompts, built...)
	}
	request, err := a.conversation.BuildRequest(ctx, prompts)
	if err != nil {
		return nil, err
	}
	for _, mw := range a.middlewares {
		request, err = mw.ModifyModelRequest(ctx, prompts, request, a.graphState)
		if err != nil {
			return nil, err
		}
	}
	err = a.event(ctx, s, "llm_requesting", "", types.LLMRequestingPayload{Messages: request})
	if err != nil {
		return nil, err
	}
	endpoint := middleware.ModelHandler(func(ctx context.Context, input []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		return a.model.Stream(ctx, input)
	})
	for i := len(a.middlewares) - 1; i >= 0; i-- {
		wrapper, ok := a.middlewares[i].(middleware.ModelMiddleware)
		if ok {
			endpoint = wrapper.WrapModel(endpoint)
		}
	}
	stream, err := endpoint(ctx, request)
	if err != nil {
		if stream != nil {
			stream.Close()
		}
		return nil, err
	}
	if stream == nil {
		return nil, fmt.Errorf("model returned nil stream")
	}
	// Middleware transfers ownership through the returned stream.
	// Close only the outermost reader: Eino wrappers close their source, and
	// the underlying reader does not support repeated Close calls.
	defer func() { stream.Close() }()
	for _, mw := range a.middlewares {
		next, modifyErr := mw.ModifyModelStreamResponse(ctx, stream, a.graphState)
		if modifyErr != nil {
			return nil, modifyErr
		}
		if next == nil {
			return nil, fmt.Errorf("middleware returned nil model stream")
		}
		stream = next
	}
	var chunks []*schema.Message
	buffer := toolCallBuffer{}
	defer func() { a.executor.snapshot(s.Calls) }()
	for {
		err := ctx.Err()
		if err != nil {
			return nil, err
		}
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if chunk == nil {
			continue
		}
		ready, collectErr := buffer.add(chunk.ToolCalls)
		if collectErr != nil {
			return nil, collectErr
		}
		messagePart := *chunk
		messagePart.ToolCalls = nil
		chunks = append(chunks, &messagePart)
		if a.eager {
			for _, call := range ready {
				started, err := a.executor.startEagerIfAllowed(ctx, call, func(ctx context.Context, call types.ToolCall, chunk string) error {
					return a.event(ctx, s, "tool_call_output_chunk", call.ID, types.ToolCallOutputChunkPayload{Name: call.Name, CallID: call.ID, Chunk: chunk})
				})
				if err != nil {
					return nil, err
				}
				if started {
					buffer.started[call.ID] = true
				}
			}
		}
		err = a.event(ctx, s, "llm_token", "", types.LLMTokenChunk{Message: chunk, Text: chunk.Content, ReasoningText: chunk.ReasoningContent})
		if err != nil {
			return nil, err
		}

	}
	err = ctx.Err()
	if err != nil {
		return nil, err
	}
	if len(chunks) == 0 {
		return nil, fmt.Errorf("empty model response")
	}
	response, err := schema.ConcatMessages(chunks)
	if err != nil {
		return nil, err
	}
	response.ToolCalls, err = buffer.finish()
	if err != nil {
		return nil, err
	}
	if response.Role == "" {
		response.Role = schema.Assistant
	}
	if response.Role != schema.Assistant {
		return nil, fmt.Errorf("invalid model role %q", response.Role)
	}
	for _, mw := range a.middlewares {
		response, err = mw.ModifyModelResponse(ctx, response, a.graphState)
		if err != nil {
			return nil, err
		}
		if response == nil {
			return nil, fmt.Errorf("middleware returned nil model message")
		}
	}
	err = a.conversation.AddHistory(ctx, s.RunID, response)
	if err != nil {
		return nil, err
	}
	usage := responseUsage(response)
	if usage != nil {
		a.conversation.RecordModelUsage(ctx, usage)
	}
	s.Usage = a.conversation.RunUsage()
	s.Calls = nil
	seen := make(map[string]bool)
	for i, call := range response.ToolCalls {
		if call.ID == "" || seen[call.ID] {
			return nil, fmt.Errorf("missing or duplicate tool call ID %q", call.ID)
		}
		seen[call.ID] = true
		s.Calls = append(s.Calls, types.ToolCallState{Call: types.ToolCall{ID: call.ID, Index: i, Name: call.Function.Name, Arguments: call.Function.Arguments}, Status: types.CallPending})
	}
	err = a.event(ctx, s, "llm_end", "", types.LLMEnd{CallbackOutput: model.CallbackOutput{Message: response}})
	if err != nil {
		return nil, err
	}
	if usage != nil {
		err = a.event(ctx, s, "tokens", "", s.Usage)
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}

// responseUsage parses provider metadata only; Conversation owns accumulation.
// Older adapters put usage in Extra. Explicit Eino metadata takes precedence.
func responseUsage(message *schema.Message) *model.TokenUsage {
	if message.ResponseMeta != nil && message.ResponseMeta.Usage != nil {
		u := message.ResponseMeta.Usage
		return &model.TokenUsage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, TotalTokens: u.TotalTokens}
	}
	number := func(value any) int {
		switch n := value.(type) {
		case int:
			return n
		case int32:
			return int(n)
		case int64:
			return int(n)
		case float64:
			return int(n)
		}
		return 0
	}
	_, prompt := message.Extra["prompt_tokens"]
	_, completion := message.Extra["completion_tokens"]
	_, total := message.Extra["total_tokens"]
	if !prompt && !completion && !total {
		return nil
	}
	usage := &model.TokenUsage{PromptTokens: number(message.Extra["prompt_tokens"]), CompletionTokens: number(message.Extra["completion_tokens"]), TotalTokens: number(message.Extra["total_tokens"])}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	return usage
}

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
			found, ok := b.byProvider[*delta.Index]
			if ok {
				index = found
			}
		}
		if delta.ID != "" {
			found, ok := b.byID[delta.ID]
			if ok {
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
			repaired, err := repairToolArguments(call.Arguments)
			if err == nil && json.Valid([]byte(repaired)) {
				call.Arguments = repaired
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
