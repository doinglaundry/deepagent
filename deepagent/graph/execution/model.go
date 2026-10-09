package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

var ErrExceedMaxModelCalls = errors.New("exceeds max model calls")

func (graph *Graph) callModel(ctx context.Context, runState *agentmodel.RunState) (*agentmodel.RunState, error) {
	if graph.config.MaxModelCalls > 0 && runState.ModelCalls >= graph.config.MaxModelCalls {
		return nil, fmt.Errorf("%w: maximum model calls exceeded: %d", ErrExceedMaxModelCalls, graph.config.MaxModelCalls)
	}
	if hasPendingInputs(runState) {
		_, err := graph.prepareConversation(ctx, runState)
		if err != nil {
			return nil, err
		}
	}
	// prepare handles new user input; tool results can independently cross the
	// context limit before the next sampling boundary in the same graph loop.
	if runState.Phase == agentmodel.PhaseTools {
		err := graph.compactContext(ctx, runState)
		if err != nil {
			return nil, err
		}
	}
	runState.Phase = agentmodel.PhaseModeling
	runState.ModelCalls++
	prompts := append([]*agentmodel.Message(nil), graph.config.Prompts...)
	for _, currentMiddleware := range graph.middlewares {
		promptMessages, err := currentMiddleware.BuildPrompt(ctx)
		if err != nil {
			return nil, err
		}
		prompts = append(prompts, promptMessages...)
	}
	requestMessages, err := graph.conversation.BuildRequest(ctx, prompts)
	if err != nil {
		return nil, err
	}
	for _, currentMiddleware := range graph.middlewares {
		requestMessages, err = currentMiddleware.ModifyModelRequest(ctx, prompts, requestMessages, graph.graphState)
		if err != nil {
			return nil, err
		}
	}
	err = graph.emitEvent(ctx, runState, "llm_requesting", "", agentmodel.LLMRequestingPayload{Messages: requestMessages})
	if err != nil {
		return nil, err
	}
	modelHandler := agentmodel.ModelHandler(func(ctx context.Context, input []*agentmodel.Message) (*schema.StreamReader[*agentmodel.Message], error) {
		stream, err := graph.chatModel.Stream(ctx, agentmodel.ToEinoMessages(input))
		if stream == nil {
			return nil, err
		}
		converted := schema.StreamReaderWithConvert(stream, func(chunk *schema.Message) (*agentmodel.Message, error) {
			return agentmodel.FromEino(chunk), nil
		})
		return converted, err
	})
	for i := len(graph.middlewares) - 1; i >= 0; i-- {
		modelMiddleware, ok := graph.middlewares[i].(agentmodel.ModelMiddleware)
		if ok {
			modelHandler = modelMiddleware.WrapModel(modelHandler)
		}
	}
	messageStream, err := modelHandler(ctx, requestMessages)
	if err != nil {
		if messageStream != nil {
			messageStream.Close()
		}
		return nil, err
	}
	if messageStream == nil {
		return nil, fmt.Errorf("model returned nil stream")
	}
	// Middleware transfers ownership through the returned stream.
	// Close only the outermost reader: Eino wrappers close their source, and
	// the underlying reader does not support repeated Close calls.
	defer func() { messageStream.Close() }()
	for _, currentMiddleware := range graph.middlewares {
		modifiedStream, modifyErr := currentMiddleware.ModifyModelStreamResponse(ctx, messageStream, graph.graphState)
		if modifyErr != nil {
			return nil, modifyErr
		}
		if modifiedStream == nil {
			return nil, fmt.Errorf("middleware returned nil model stream")
		}
		messageStream = modifiedStream
	}
	var messageChunks []*agentmodel.Message
	toolCallBuffer := toolCallBuffer{}
	defer func() { graph.toolExecutor.snapshotToolExecutions(runState.Calls) }()
	for {
		err := ctx.Err()
		if err != nil {
			return nil, err
		}
		chunk, err := messageStream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if chunk == nil {
			continue
		}
		readyToolCalls, collectErr := toolCallBuffer.appendToolCallFragments(chunk.ToolCalls)
		if collectErr != nil {
			return nil, collectErr
		}
		messagePart := *chunk
		messagePart.ToolCalls = nil
		messageChunks = append(messageChunks, &messagePart)
		if graph.enableEagerTools {
			for _, toolCall := range readyToolCalls {
				hasStartedEagerTool, err := graph.toolExecutor.startEagerToolIfAllowed(ctx, toolCall, func(ctx context.Context, toolCall agentmodel.ToolCall, chunk string) error {
					return graph.emitEvent(ctx, runState, "tool_call_output_chunk", toolCall.ID, agentmodel.ToolCallOutputChunkPayload{Name: toolCall.Name, CallID: toolCall.ID, Chunk: chunk})
				})
				if err != nil {
					return nil, err
				}
				if hasStartedEagerTool {
					toolCallBuffer.eagerStartedByCallID[toolCall.ID] = true
				}
			}
		}
		err = graph.emitEvent(ctx, runState, "llm_token", "", agentmodel.LLMTokenChunk{Message: chunk, Text: chunk.Content, ReasoningText: chunk.ReasoningContent})
		if err != nil {
			return nil, err
		}

	}
	err = ctx.Err()
	if err != nil {
		return nil, err
	}
	if len(messageChunks) == 0 {
		return nil, fmt.Errorf("empty model response")
	}
	response, err := agentmodel.ConcatMessages(messageChunks)
	if err != nil {
		return nil, err
	}
	response.ToolCalls, err = toolCallBuffer.buildToolCalls()
	if err != nil {
		return nil, err
	}
	if response.Role == "" {
		response.Role = schema.Assistant
	}
	if response.Role != schema.Assistant {
		return nil, fmt.Errorf("invalid model role %q", response.Role)
	}
	for _, currentMiddleware := range graph.middlewares {
		response, err = currentMiddleware.ModifyModelResponse(ctx, response, graph.graphState)
		if err != nil {
			return nil, err
		}
		if response == nil {
			return nil, fmt.Errorf("middleware returned nil model message")
		}
	}
	err = graph.conversation.AddHistory(ctx, runState.RunID, response)
	if err != nil {
		return nil, err
	}
	tokenUsage := getResponseUsage(response)
	if tokenUsage != nil {
		graph.conversation.RecordModelUsage(ctx, tokenUsage)
	}
	runState.Usage = graph.conversation.GetRunUsage()
	runState.Calls = nil
	seenCallIDs := make(map[string]bool)
	for i, toolCall := range response.ToolCalls {
		if toolCall.ID == "" || seenCallIDs[toolCall.ID] {
			return nil, fmt.Errorf("missing or duplicate tool call ID %q", toolCall.ID)
		}
		seenCallIDs[toolCall.ID] = true
		runState.Calls = append(runState.Calls, agentmodel.ToolCallState{Call: agentmodel.ToolCall{ID: toolCall.ID, Index: i, Name: toolCall.Function.Name, Arguments: toolCall.Function.Arguments}, Status: agentmodel.CallPending})
	}
	err = graph.emitEvent(ctx, runState, "llm_end", "", agentmodel.LLMEnd{Message: response})
	if err != nil {
		return nil, err
	}
	if tokenUsage != nil {
		err = graph.emitEvent(ctx, runState, "tokens", "", runState.Usage)
		if err != nil {
			return nil, err
		}
	}
	return runState, nil
}

// getResponseUsage parses provider metadata only; Conversation owns accumulation.
// Older adapters put usage in Extra. Explicit Eino metadata takes precedence.
func getResponseUsage(message *agentmodel.Message) *model.TokenUsage {
	if message.ResponseMeta != nil && message.ResponseMeta.Usage != nil {
		providerUsage := message.ResponseMeta.Usage
		return &model.TokenUsage{PromptTokens: providerUsage.PromptTokens, CompletionTokens: providerUsage.CompletionTokens, TotalTokens: providerUsage.TotalTokens}
	}
	parseTokenCount := func(value any) int {
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
	_, hasPromptTokens := message.Extra["prompt_tokens"]
	_, hasCompletionTokens := message.Extra["completion_tokens"]
	_, hasTotalTokens := message.Extra["total_tokens"]
	if !hasPromptTokens && !hasCompletionTokens && !hasTotalTokens {
		return nil
	}
	tokenUsage := &model.TokenUsage{PromptTokens: parseTokenCount(message.Extra["prompt_tokens"]), CompletionTokens: parseTokenCount(message.Extra["completion_tokens"]), TotalTokens: parseTokenCount(message.Extra["total_tokens"])}
	if tokenUsage.TotalTokens == 0 {
		tokenUsage.TotalTokens = tokenUsage.PromptTokens + tokenUsage.CompletionTokens
	}
	return tokenUsage
}

// toolCallBuffer lives for exactly one model stream. Provider indexes are map
// keys; the index used by the executor is always the contiguous arrival order.
type toolCallBuffer struct {
	toolCalls                  []agentmodel.ToolCall
	callIndexesByProviderIndex map[int]int
	callIndexesByCallID        map[string]int
	eagerStartedByCallID       map[string]bool
}

func (toolCallBuffer *toolCallBuffer) appendToolCallFragments(deltas []schema.ToolCall) ([]agentmodel.ToolCall, error) {
	if toolCallBuffer.callIndexesByProviderIndex == nil {
		toolCallBuffer.callIndexesByProviderIndex = map[int]int{}
		toolCallBuffer.callIndexesByCallID = map[string]int{}
		toolCallBuffer.eagerStartedByCallID = map[string]bool{}
	}
	var readyToolCalls []agentmodel.ToolCall
	for _, delta := range deltas {
		index := -1
		if delta.Index != nil {
			found, ok := toolCallBuffer.callIndexesByProviderIndex[*delta.Index]
			if ok {
				index = found
			}
		}
		if delta.ID != "" {
			found, ok := toolCallBuffer.callIndexesByCallID[delta.ID]
			if ok {
				if index >= 0 && index != found {
					return nil, fmt.Errorf("conflicting tool ID and provider index")
				}
				index = found
			}
		}
		if index < 0 {
			if delta.Index == nil && delta.ID == "" {
				if len(toolCallBuffer.toolCalls) != 1 {
					return nil, fmt.Errorf("ambiguous tool fragment without identity")
				}
				index = 0
			} else {
				index = len(toolCallBuffer.toolCalls)
				toolCallBuffer.toolCalls = append(toolCallBuffer.toolCalls, agentmodel.ToolCall{Index: index})
			}
		}
		call := &toolCallBuffer.toolCalls[index]
		if delta.Index != nil {
			toolCallBuffer.callIndexesByProviderIndex[*delta.Index] = index
		}
		if delta.ID != "" {
			if call.ID != "" && call.ID != delta.ID {
				return nil, fmt.Errorf("tool call ID changed")
			}
			call.ID = delta.ID
			toolCallBuffer.callIndexesByCallID[delta.ID] = index
		}
		if delta.Function.Name != "" {
			if call.Name == "" {
				call.Name = delta.Function.Name
			} else if call.Name != delta.Function.Name {
				call.Name += delta.Function.Name
			}
		}
		if toolCallBuffer.eagerStartedByCallID[call.ID] && strings.TrimSpace(delta.Function.Arguments) != "" {
			return nil, fmt.Errorf("tool arguments changed after eager execution")
		}
		call.Arguments += delta.Function.Arguments
		if call.ID != "" && call.Name != "" && json.Valid([]byte(call.Arguments)) && !toolCallBuffer.eagerStartedByCallID[call.ID] {
			readyToolCalls = append(readyToolCalls, *call)
		}
	}
	return readyToolCalls, nil
}

func (toolCallBuffer *toolCallBuffer) buildToolCalls() ([]schema.ToolCall, error) {
	result := make([]schema.ToolCall, 0, len(toolCallBuffer.toolCalls))
	for _, toolCall := range toolCallBuffer.toolCalls {
		if toolCall.ID == "" || toolCall.Name == "" {
			return nil, fmt.Errorf("incomplete tool call identity")
		}
		// Only repair after EOF: a partial fragment may still change. Preserve
		// unrepairable arguments so the tool can report its validation error.
		if !json.Valid([]byte(toolCall.Arguments)) {
			repaired, err := repairToolArguments(toolCall.Arguments)
			if err == nil && json.Valid([]byte(repaired)) {
				toolCall.Arguments = repaired
			}
		}
		index := toolCall.Index
		result = append(result, schema.ToolCall{ID: toolCall.ID, Index: &index, Type: "function", Function: schema.FunctionCall{Name: toolCall.Name, Arguments: toolCall.Arguments}})
	}
	return result, nil
}

// repairToolArguments strips an enclosing Markdown code fence and trailing commas only.
// It never invents missing field names, quotes, values, or delimiters.
func repairToolArguments(input string) (string, error) {
	arguments := strings.TrimSpace(input)
	if strings.HasPrefix(arguments, "```") && strings.HasSuffix(arguments, "```") {
		i := strings.IndexByte(arguments, '\n')
		if i >= 0 {
			arguments = strings.TrimSpace(arguments[i+1 : len(arguments)-3])
		}
	}
	var argumentsBuilder strings.Builder
	quoted, escaped := false, false
	for i, r := range arguments {
		if quoted {
			argumentsBuilder.WriteRune(r)
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
			argumentsBuilder.WriteRune(r)
			continue
		}
		if r == ',' {
			j := i + 1
			for j < len(arguments) && unicode.IsSpace(rune(arguments[j])) {
				j++
			}
			if j < len(arguments) && (arguments[j] == '}' || arguments[j] == ']') {
				continue
			}
		}
		argumentsBuilder.WriteRune(r)
	}
	repairedArguments := argumentsBuilder.String()
	if !json.Valid([]byte(repairedArguments)) {
		return "", errors.New("invalid JSON arguments; only code fences and trailing commas can be repaired")
	}
	return repairedArguments, nil
}
