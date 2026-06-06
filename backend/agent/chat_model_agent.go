package agent

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type chatModelAgentConfig struct {
	Name                string
	Description         string
	Instruction         string
	Model               model.BaseChatModel
	ToolsConfig         adk.ToolsConfig
	MaxIterations       int
	Handlers            []adk.ChatModelAgentMiddleware
	ModelRetryConfig    *adk.ModelRetryConfig
	ModelFailoverConfig *adk.ModelFailoverConfig
	OutputKey           string
}

type chatModelAgent struct {
	name                string
	description         string
	instruction         string
	model               model.BaseChatModel
	toolsConfig         adk.ToolsConfig
	maxIterations       int
	handlers            []adk.ChatModelAgentMiddleware
	modelRetryConfig    *adk.ModelRetryConfig
	modelFailoverConfig *adk.ModelFailoverConfig
	outputKey           string
}

type chatModelRun struct {
	agent          *chatModelAgent
	instruction    string
	tools          []tool.BaseTool
	returnDirectly map[string]bool
	toolInfos      []*schema.ToolInfo
	state          *adk.ChatModelAgentState
	generator      *adk.AsyncGenerator[*adk.AgentEvent]
}

type toolCallResult struct {
	index   int
	call    schema.ToolCall
	message adk.Message
	err     error
}

func newChatModelAgent(_ context.Context, cfg chatModelAgentConfig) (adk.ResumableAgent, error) {
	if cfg.Name == "" {
		return nil, fmt.Errorf("agent name is required")
	}
	if cfg.Model == nil {
		return nil, fmt.Errorf("chat model is required")
	}
	maxIterations := cfg.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 20
	}
	return &chatModelAgent{
		name:                cfg.Name,
		description:         cfg.Description,
		instruction:         cfg.Instruction,
		model:               cfg.Model,
		toolsConfig:         cfg.ToolsConfig,
		maxIterations:       maxIterations,
		handlers:            append([]adk.ChatModelAgentMiddleware(nil), cfg.Handlers...),
		modelRetryConfig:    cfg.ModelRetryConfig,
		modelFailoverConfig: cfg.ModelFailoverConfig,
		outputKey:           cfg.OutputKey,
	}, nil
}

func (a *chatModelAgent) Name(context.Context) string {
	return a.name
}

func (a *chatModelAgent) Description(context.Context) string {
	return a.description
}

func (a *chatModelAgent) Run(
	ctx context.Context,
	input *adk.AgentInput,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	go func() {
		defer gen.Close()
		if input == nil {
			gen.Send(a.errorEvent(fmt.Errorf("agent input is required")))
			return
		}
		a.run(ctx, input, gen)
	}()
	return iter
}

func (a *chatModelAgent) Resume(
	_ context.Context,
	_ *adk.ResumeInfo,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	go func() {
		defer gen.Close()
		gen.Send(a.errorEvent(fmt.Errorf("chat model agent does not support resume yet")))
	}()
	return iter
}

func (a *chatModelAgent) run(
	ctx context.Context,
	input *adk.AgentInput,
	gen *adk.AsyncGenerator[*adk.AgentEvent],
) {
	run, err := a.prepareRun(ctx, input, gen)
	if err != nil {
		gen.Send(a.errorEvent(err))
		return
	}

	for remaining := run.agent.maxIterations; remaining > 0; remaining-- {
		modelMessage, err := run.callModel(ctx, input.EnableStreaming)
		if err != nil {
			gen.Send(a.errorEvent(err))
			return
		}
		if modelMessage == nil {
			gen.Send(a.errorEvent(fmt.Errorf("chat model returned nil message")))
			return
		}

		run.state.Messages = append(run.state.Messages, modelMessage)
		if err := run.afterModel(ctx); err != nil {
			gen.Send(a.errorEvent(err))
			return
		}
		modelMessage = run.state.Messages[len(run.state.Messages)-1]

		run.sendMessageEvent(modelMessage, schema.Assistant, "")
		if len(modelMessage.ToolCalls) == 0 {
			return
		}

		toolMessages, err := run.callTools(ctx, modelMessage.ToolCalls)
		if err != nil {
			gen.Send(a.errorEvent(err))
			return
		}
		run.state.Messages = append(run.state.Messages, toolMessages...)
		if err := run.afterToolCalls(ctx, modelMessage.ToolCalls); err != nil {
			gen.Send(a.errorEvent(err))
			return
		}
		if direct := run.returnDirectMessage(modelMessage.ToolCalls, toolMessages); direct != nil {
			run.sendMessageEvent(direct, schema.Assistant, "")
			return
		}
	}

	gen.Send(a.errorEvent(fmt.Errorf("exceeded max iterations: %d", run.agent.maxIterations)))
}

func (a *chatModelAgent) prepareRun(
	ctx context.Context,
	input *adk.AgentInput,
	gen *adk.AsyncGenerator[*adk.AgentEvent],
) (*chatModelRun, error) {
	runCtx := &adk.ChatModelAgentContext{
		Instruction:    a.instruction,
		Tools:          append([]tool.BaseTool(nil), a.toolsConfig.Tools...),
		ReturnDirectly: copyBoolMap(a.toolsConfig.ReturnDirectly),
	}
	for _, handler := range a.handlers {
		var err error
		ctx, runCtx, err = handler.BeforeAgent(ctx, runCtx)
		if err != nil {
			return nil, err
		}
		if runCtx == nil {
			return nil, fmt.Errorf("handler returned nil run context")
		}
	}

	infos, err := getToolInfos(ctx, runCtx.Tools)
	if err != nil {
		return nil, err
	}
	return &chatModelRun{
		agent:          a,
		instruction:    runCtx.Instruction,
		tools:          runCtx.Tools,
		returnDirectly: runCtx.ReturnDirectly,
		toolInfos:      infos,
		state: &adk.ChatModelAgentState{
			Messages: append([]adk.Message(nil), input.Messages...),
		},
		generator: gen,
	}, nil
}

func (r *chatModelRun) callModel(ctx context.Context, streaming bool) (adk.Message, error) {
	if err := r.beforeModel(ctx); err != nil {
		return nil, err
	}
	messages := make([]adk.Message, 0, len(r.state.Messages)+1)
	if r.instruction != "" {
		messages = append(messages, schema.SystemMessage(r.instruction))
	}
	messages = append(messages, r.state.Messages...)

	callModel := r.agent.model
	opts := []model.Option{model.WithTools(r.toolInfos)}
	if withTools, ok := callModel.(model.ToolCallingChatModel); ok {
		boundModel, err := withTools.WithTools(r.toolInfos)
		if err != nil {
			return nil, err
		}
		callModel = boundModel
		opts = nil
	}
	var err error
	callModel, err = r.wrapModel(ctx, callModel)
	if err != nil {
		return nil, err
	}

	if streaming {
		reader, err := callModel.Stream(ctx, messages, opts...)
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		var chunks []adk.Message
		for {
			chunk, err := reader.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			if chunk != nil {
				chunks = append(chunks, chunk)
			}
		}
		return schema.ConcatMessages(chunks)
	}
	return callModel.Generate(ctx, messages, opts...)
}

func (r *chatModelRun) wrapModel(ctx context.Context, chatModel model.BaseChatModel) (model.BaseChatModel, error) {
	modelCtx := r.modelContext()
	wrapped := chatModel
	for i := len(r.agent.handlers) - 1; i >= 0; i-- {
		next, err := r.agent.handlers[i].WrapModel(ctx, wrapped, modelCtx)
		if err != nil {
			return nil, err
		}
		if next != nil {
			wrapped = next
		}
	}
	return wrapped, nil
}

func (r *chatModelRun) beforeModel(ctx context.Context) error {
	modelCtx := r.modelContext()
	for _, handler := range r.agent.handlers {
		var err error
		ctx, r.state, err = handler.BeforeModelRewriteState(ctx, r.state, modelCtx)
		if err != nil {
			return err
		}
		if r.state == nil {
			return fmt.Errorf("handler returned nil model state before model call")
		}
	}
	return nil
}

func (r *chatModelRun) afterModel(ctx context.Context) error {
	modelCtx := r.modelContext()
	for _, handler := range r.agent.handlers {
		var err error
		ctx, r.state, err = handler.AfterModelRewriteState(ctx, r.state, modelCtx)
		if err != nil {
			return err
		}
		if r.state == nil {
			return fmt.Errorf("handler returned nil model state after model call")
		}
	}
	return nil
}

func (r *chatModelRun) afterToolCalls(ctx context.Context, toolCalls []schema.ToolCall) error {
	toolCtx := &adk.ToolCallsContext{ToolCalls: make([]adk.ToolContext, 0, len(toolCalls))}
	for _, call := range toolCalls {
		toolCtx.ToolCalls = append(toolCtx.ToolCalls, adk.ToolContext{
			Name:   call.Function.Name,
			CallID: call.ID,
		})
	}
	for _, handler := range r.agent.handlers {
		var err error
		ctx, r.state, err = handler.AfterToolCallsRewriteState(ctx, r.state, toolCtx)
		if err != nil {
			return err
		}
		if r.state == nil {
			return fmt.Errorf("handler returned nil model state after tool calls")
		}
	}
	return nil
}

func (r *chatModelRun) callTools(ctx context.Context, toolCalls []schema.ToolCall) ([]adk.Message, error) {
	results := make([]toolCallResult, len(toolCalls))
	var wg sync.WaitGroup
	for i, call := range toolCalls {
		wg.Add(1)
		go func(index int, toolCall schema.ToolCall) {
			defer wg.Done()
			msg, err := r.callTool(ctx, toolCall)
			results[index] = toolCallResult{
				index:   index,
				call:    toolCall,
				message: msg,
				err:     err,
			}
		}(i, call)
	}
	wg.Wait()

	messages := make([]adk.Message, len(results))
	for _, result := range results {
		if result.err != nil {
			return nil, result.err
		}
		messages[result.index] = result.message
		r.sendMessageEvent(result.message, schema.Tool, result.call.Function.Name)
	}
	return messages, nil
}

func (r *chatModelRun) callTool(ctx context.Context, toolCall schema.ToolCall) (adk.Message, error) {
	selected := findTool(ctx, r.tools, toolCall.Function.Name)
	if selected == nil {
		return nil, fmt.Errorf("tool %q not found", toolCall.Function.Name)
	}
	invokable, ok := selected.(tool.InvokableTool)
	if !ok {
		return nil, fmt.Errorf("tool %q is not invokable", toolCall.Function.Name)
	}

	endpoint := invokable.InvokableRun
	toolCtx := &adk.ToolContext{Name: toolCall.Function.Name, CallID: toolCall.ID}
	for i := len(r.agent.handlers) - 1; i >= 0; i-- {
		wrapped, err := r.agent.handlers[i].WrapInvokableToolCall(ctx, endpoint, toolCtx)
		if err != nil {
			return nil, err
		}
		if wrapped != nil {
			endpoint = wrapped
		}
	}

	output, err := endpoint(ctx, toolCall.Function.Arguments)
	if err != nil {
		return nil, err
	}
	return schema.ToolMessage(output, toolCall.ID, schema.WithToolName(toolCall.Function.Name)), nil
}

func (r *chatModelRun) modelContext() *adk.ModelContext {
	return &adk.ModelContext{
		Tools:               append([]*schema.ToolInfo(nil), r.toolInfos...),
		ModelRetryConfig:    r.agent.modelRetryConfig,
		ModelFailoverConfig: r.agent.modelFailoverConfig,
	}
}

func (r *chatModelRun) returnDirectMessage(toolCalls []schema.ToolCall, toolMessages []adk.Message) adk.Message {
	for _, call := range toolCalls {
		if !r.returnDirectly[call.Function.Name] {
			continue
		}
		for _, msg := range toolMessages {
			if msg != nil && msg.ToolCallID == call.ID {
				return schema.AssistantMessage(msg.Content, nil)
			}
		}
	}
	return nil
}

func (r *chatModelRun) sendMessageEvent(msg adk.Message, role schema.RoleType, toolName string) {
	if msg == nil {
		return
	}
	r.generator.Send(&adk.AgentEvent{
		AgentName: r.agent.name,
		Output: &adk.AgentOutput{
			MessageOutput: &adk.MessageVariant{
				Message:  msg,
				Role:     role,
				ToolName: toolName,
			},
		},
	})
}

func (a *chatModelAgent) errorEvent(err error) *adk.AgentEvent {
	return &adk.AgentEvent{AgentName: a.name, Err: err}
}

func getToolInfos(ctx context.Context, tools []tool.BaseTool) ([]*schema.ToolInfo, error) {
	infos := make([]*schema.ToolInfo, 0, len(tools))
	for _, candidate := range tools {
		info, err := candidate.Info(ctx)
		if err != nil {
			return nil, err
		}
		if info != nil {
			infos = append(infos, info)
		}
	}
	return infos, nil
}

func findTool(ctx context.Context, tools []tool.BaseTool, name string) tool.BaseTool {
	for _, candidate := range tools {
		info, err := candidate.Info(ctx)
		if err == nil && info != nil && info.Name == name {
			return candidate
		}
	}
	return nil
}

func copyBoolMap(in map[string]bool) map[string]bool {
	if in == nil {
		return nil
	}
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
