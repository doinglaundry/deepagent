package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"

	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

var ErrExceedMaxModelCalls = errors.New("exceeds max model calls")

func (graph *Graph) callModel(ctx context.Context, runState *agentmodel.RunState) (_ *agentmodel.RunState, err error) {
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
	// 留最后一次调用整理结果，避免工具循环把全部次数耗光后只返回报错。
	finalAnswerOnly := (graph.config.MaxModelCalls > 1 && runState.ModelCalls == graph.config.MaxModelCalls) || runState.WebSearchFailures >= 3 || runState.WebSearchDuration >= 2*time.Minute
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
	if finalAnswerOnly {
		requestMessages = append([]*agentmodel.Message{agentmodel.NewSystemMessage("The execution or Web research budget has been reached. Summarize the results already obtained, state what could not be verified and why, and provide a useful final answer. Do not call any tools or invent missing results.")}, requestMessages...)
	}
	// 中间件可能修改内容或提前拒绝响应；单独保留模型边界收到的原始分片。
	// 锁保护可能预读流的中间件；结束时只发布已经收到的快照，不等待剩余网络数据。
	var rawResponses []*agentmodel.LLMResponsePayload
	var rawResponseMutex sync.Mutex
	defer func() {
		rawResponseMutex.Lock()
		responses := make([]agentmodel.LLMResponsePayload, 0, len(rawResponses))
		for _, response := range rawResponses {
			responses = append(responses, agentmodel.LLMResponsePayload{LLMResponseID: response.LLMResponseID, Chunks: append([]*agentmodel.Message(nil), response.Chunks...)})
		}
		rawResponseMutex.Unlock()
		// 用户停止后也保存已接收内容，收尾最多等两秒。
		saveContext, cancelSave := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancelSave()
		for _, response := range responses {
			if len(response.Chunks) > 0 {
				err = errors.Join(err, graph.emitEvent(saveContext, runState, "llm_response", "", response))
			}
		}
	}()
	modelHandler := agentmodel.ModelHandler(func(ctx context.Context, input []*agentmodel.Message) (*schema.StreamReader[*agentmodel.Message], error) {
		// 放在最终调用边界，保留 WrapModel 修改后的请求和每次重试。
		toolInfos, err := graph.toolSet.GetToolInfos(ctx)
		if err != nil {
			return nil, err
		}
		response := &agentmodel.LLMResponsePayload{LLMResponseID: uuid.NewString()}
		rawResponseMutex.Lock()
		rawResponses = append(rawResponses, response)
		rawResponseMutex.Unlock()
		request := agentmodel.LLMRequestingPayload{Messages: input, Tools: toolInfos, LLMResponseID: response.LLMResponseID}
		if finalAnswerOnly {
			request.ToolChoice = schema.ToolChoiceForbidden
		}
		err = graph.emitEvent(ctx, runState, "llm_requesting", "", request)
		if err != nil {
			return nil, err
		}
		var options []model.Option
		if finalAnswerOnly {
			options = append(options, model.WithToolChoice(schema.ToolChoiceForbidden))
		}
		stream, err := graph.chatModel.Stream(ctx, agentmodel.ToEinoMessages(input), options...)
		if stream == nil {
			return nil, err
		}
		converted := schema.StreamReaderWithConvert(stream, func(chunk *schema.Message) (*agentmodel.Message, error) {
			message := agentmodel.FromEino(chunk)
			snapshot := agentmodel.CopyMessage(message)
			if message != nil && snapshot == nil {
				return nil, errors.New("cannot snapshot model response")
			}
			rawResponseMutex.Lock()
			if snapshot != nil {
				response.Chunks = append(response.Chunks, snapshot)
			}
			rawResponseMutex.Unlock()
			return message, nil
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
		if graph.enableEagerTools && !finalAnswerOnly {
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
	if finalAnswerOnly && len(response.ToolCalls) > 0 {
		return nil, fmt.Errorf("%w: final response must not call tools", ErrExceedMaxModelCalls)
	}
	// 角色、用量和推理分片不代表已经生成答案，不能据此宣告任务完成。
	if strings.TrimSpace(response.Content) == "" && len(response.ToolCalls) == 0 && len(response.MultiContent) == 0 && len(response.AssistantGenMultiContent) == 0 {
		return nil, fmt.Errorf("empty model response")
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
