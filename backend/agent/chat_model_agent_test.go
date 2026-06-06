package agent

import (
	"context"
	"fmt"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"eino-cli/backend/agent/middlewares"
)

type agentTestModel struct {
	calls int
}

func (m *agentTestModel) Generate(
	_ context.Context,
	input []*schema.Message,
	_ ...model.Option,
) (*schema.Message, error) {
	m.calls++
	switch m.calls {
	case 1:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID: "call-1",
			Function: schema.FunctionCall{
				Name:      "echo",
				Arguments: `{"value":"hello"}`,
			},
		}}), nil
	case 2:
		for _, msg := range input {
			if msg.Role == schema.Tool && msg.ToolCallID == "call-1" {
				return schema.AssistantMessage("final: "+msg.Content, nil), nil
			}
		}
		return nil, fmt.Errorf("second model call did not receive tool message")
	default:
		return nil, fmt.Errorf("unexpected model call %d", m.calls)
	}
}

func (m *agentTestModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

type echoTool struct{}

func (echoTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "echo"}, nil
}

func (echoTool) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	return "hello", nil
}

type scriptedModel struct {
	responses []*schema.Message
	calls     int
}

func (m *scriptedModel) Generate(
	_ context.Context,
	_ []*schema.Message,
	_ ...model.Option,
) (*schema.Message, error) {
	if m.calls >= len(m.responses) {
		return nil, fmt.Errorf("unexpected model call %d", m.calls+1)
	}
	msg := m.responses[m.calls]
	m.calls++
	return msg, nil
}

func (m *scriptedModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

type countingTool struct {
	name   string
	output string
	calls  int
	args   []string
}

func (t *countingTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name}, nil
}

func (t *countingTool) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	t.calls++
	return t.output, nil
}

func (t *countingTool) InvokableRunWithArgs(_ context.Context, args string, _ ...tool.Option) (string, error) {
	t.calls++
	t.args = append(t.args, args)
	return t.output, nil
}

type argumentCapturingTool struct {
	countingTool
}

func (t *argumentCapturingTool) InvokableRun(ctx context.Context, args string, opts ...tool.Option) (string, error) {
	return t.InvokableRunWithArgs(ctx, args, opts...)
}

type streamTool struct {
	name   string
	chunks []string
	calls  int
}

func (t *streamTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name}, nil
}

func (t *streamTool) StreamableRun(_ context.Context, _ string, _ ...tool.Option) (*schema.StreamReader[string], error) {
	t.calls++
	return schema.StreamReaderFromArray(t.chunks), nil
}

func TestChatModelAgentRunsModelToolLoop(t *testing.T) {
	ctx := context.Background()
	agent, err := newChatModelAgent(ctx, chatModelAgentConfig{
		Name:          "chat",
		Description:   "test agent",
		Model:         &agentTestModel{},
		MaxIterations: 3,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{echoTool{}},
			},
		},
	})
	if err != nil {
		t.Fatalf("newChatModelAgent() error = %v", err)
	}

	iter := agent.Run(ctx, &adk.AgentInput{
		Messages: []adk.Message{schema.UserMessage("run echo")},
	})

	var final string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			t.Fatalf("agent event error = %v", event.Err)
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		msg, err := event.Output.MessageOutput.GetMessage()
		if err != nil {
			t.Fatalf("GetMessage() error = %v", err)
		}
		if msg != nil && msg.Role == schema.Assistant && len(msg.ToolCalls) == 0 {
			final = msg.Content
		}
	}

	if final != "final: hello" {
		t.Fatalf("final output = %q, want %q", final, "final: hello")
	}
}

func TestChatModelAgentClarificationEndsLoop(t *testing.T) {
	ctx := context.Background()
	agent, err := newChatModelAgent(ctx, chatModelAgentConfig{
		Name:          "chat",
		Description:   "test agent",
		Model:         &scriptedModel{responses: []*schema.Message{clarificationMessage("Which repo?")}},
		MaxIterations: 3,
		Handlers: []adk.ChatModelAgentMiddleware{
			middlewares.NewClarification(),
		},
	})
	if err != nil {
		t.Fatalf("newChatModelAgent() error = %v", err)
	}

	events := collectAgentTestEvents(t, agent.Run(ctx, &adk.AgentInput{
		Messages: []adk.Message{schema.UserMessage("inspect it")},
	}))

	final := lastAssistantContent(t, events)
	if final != "Which repo?" {
		t.Fatalf("final output = %q, want %q", final, "Which repo?")
	}
	if len(events) != 1 {
		t.Fatalf("clarification should end without tool events, got %d events", len(events))
	}
}

func TestChatModelAgentHITLDenySkipsToolExecution(t *testing.T) {
	ctx := context.Background()
	echo := &countingTool{name: "echo", output: "should not run"}
	agent, err := newChatModelAgent(ctx, chatModelAgentConfig{
		Name:          "chat",
		Description:   "test agent",
		Model:         &scriptedModel{responses: []*schema.Message{toolCallMessage("echo")}},
		MaxIterations: 3,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{echo},
			},
		},
		Handlers: []adk.ChatModelAgentMiddleware{
			middlewares.NewHITL([]string{"echo"}, func(context.Context, string, string) bool {
				return false
			}),
		},
	})
	if err != nil {
		t.Fatalf("newChatModelAgent() error = %v", err)
	}

	events := collectAgentTestEvents(t, agent.Run(ctx, &adk.AgentInput{
		Messages: []adk.Message{schema.UserMessage("run echo")},
	}))

	if echo.calls != 0 {
		t.Fatalf("denied tool was executed %d times", echo.calls)
	}
	final := lastAssistantContent(t, events)
	if final == "" {
		t.Fatalf("expected denial assistant content")
	}
}

func TestChatModelAgentReturnDirectlyUsesToolResult(t *testing.T) {
	ctx := context.Background()
	echo := &countingTool{name: "echo", output: "direct result"}
	agent, err := newChatModelAgent(ctx, chatModelAgentConfig{
		Name:          "chat",
		Description:   "test agent",
		Model:         &scriptedModel{responses: []*schema.Message{toolCallMessage("echo")}},
		MaxIterations: 3,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{echo},
			},
			ReturnDirectly: map[string]bool{"echo": true},
		},
	})
	if err != nil {
		t.Fatalf("newChatModelAgent() error = %v", err)
	}

	events := collectAgentTestEvents(t, agent.Run(ctx, &adk.AgentInput{
		Messages: []adk.Message{schema.UserMessage("run echo")},
	}))

	if echo.calls != 1 {
		t.Fatalf("tool calls = %d, want 1", echo.calls)
	}
	final := lastAssistantContent(t, events)
	if final != "direct result" {
		t.Fatalf("final output = %q, want %q", final, "direct result")
	}
}

func TestChatModelAgentAppliesToolCallMiddlewares(t *testing.T) {
	ctx := context.Background()
	echo := &countingTool{name: "echo", output: "raw result"}
	agent, err := newChatModelAgent(ctx, chatModelAgentConfig{
		Name:          "chat",
		Description:   "test agent",
		Model:         &scriptedModel{responses: []*schema.Message{toolCallMessage("echo")}},
		MaxIterations: 3,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{echo},
				ToolCallMiddlewares: []compose.ToolMiddleware{
					{
						Invokable: func(endpoint compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
							return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
								if _, err := endpoint(ctx, input); err != nil {
									return nil, err
								}
								return &compose.ToolOutput{Result: "wrapped result"}, nil
							}
						},
					},
				},
			},
			ReturnDirectly: map[string]bool{"echo": true},
		},
	})
	if err != nil {
		t.Fatalf("newChatModelAgent() error = %v", err)
	}

	events := collectAgentTestEvents(t, agent.Run(ctx, &adk.AgentInput{
		Messages: []adk.Message{schema.UserMessage("run echo")},
	}))

	if echo.calls != 1 {
		t.Fatalf("tool calls = %d, want 1", echo.calls)
	}
	final := lastAssistantContent(t, events)
	if final != "wrapped result" {
		t.Fatalf("final output = %q, want %q", final, "wrapped result")
	}
}

func TestChatModelAgentAppliesToolArgumentsHandler(t *testing.T) {
	ctx := context.Background()
	echo := &argumentCapturingTool{countingTool: countingTool{name: "echo", output: "handled"}}
	agent, err := newChatModelAgent(ctx, chatModelAgentConfig{
		Name:          "chat",
		Description:   "test agent",
		Model:         &scriptedModel{responses: []*schema.Message{toolCallMessage("echo")}},
		MaxIterations: 3,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{echo},
				ToolArgumentsHandler: func(_ context.Context, name, arguments string) (string, error) {
					if name != "echo" {
						t.Fatalf("tool argument handler name = %q, want echo", name)
					}
					if arguments != `{"value":"hello"}` {
						t.Fatalf("tool argument handler args = %q", arguments)
					}
					return `{"value":"rewritten"}`, nil
				},
			},
			ReturnDirectly: map[string]bool{"echo": true},
		},
	})
	if err != nil {
		t.Fatalf("newChatModelAgent() error = %v", err)
	}

	events := collectAgentTestEvents(t, agent.Run(ctx, &adk.AgentInput{
		Messages: []adk.Message{schema.UserMessage("run echo")},
	}))

	if echo.calls != 1 {
		t.Fatalf("tool calls = %d, want 1", echo.calls)
	}
	if len(echo.args) != 1 || echo.args[0] != `{"value":"rewritten"}` {
		t.Fatalf("tool args = %v, want rewritten", echo.args)
	}
	if final := lastAssistantContent(t, events); final != "handled" {
		t.Fatalf("final output = %q, want handled", final)
	}
}

func TestChatModelAgentUsesUnknownToolsHandler(t *testing.T) {
	ctx := context.Background()
	agent, err := newChatModelAgent(ctx, chatModelAgentConfig{
		Name:          "chat",
		Description:   "test agent",
		Model:         &scriptedModel{responses: []*schema.Message{toolCallMessage("missing")}},
		MaxIterations: 3,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				UnknownToolsHandler: func(_ context.Context, name, input string) (string, error) {
					if name != "missing" {
						t.Fatalf("unknown tool name = %q, want missing", name)
					}
					if input != `{"value":"hello"}` {
						t.Fatalf("unknown tool input = %q", input)
					}
					return "handled missing tool", nil
				},
			},
			ReturnDirectly: map[string]bool{"missing": true},
		},
	})
	if err != nil {
		t.Fatalf("newChatModelAgent() error = %v", err)
	}

	events := collectAgentTestEvents(t, agent.Run(ctx, &adk.AgentInput{
		Messages: []adk.Message{schema.UserMessage("run missing")},
	}))

	if final := lastAssistantContent(t, events); final != "handled missing tool" {
		t.Fatalf("final output = %q, want handled missing tool", final)
	}
}

func TestChatModelAgentRunsStreamableTool(t *testing.T) {
	ctx := context.Background()
	echo := &streamTool{name: "echo", chunks: []string{"stream", " result"}}
	agent, err := newChatModelAgent(ctx, chatModelAgentConfig{
		Name:          "chat",
		Description:   "test agent",
		Model:         &scriptedModel{responses: []*schema.Message{toolCallMessage("echo")}},
		MaxIterations: 3,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{echo},
			},
			ReturnDirectly: map[string]bool{"echo": true},
		},
	})
	if err != nil {
		t.Fatalf("newChatModelAgent() error = %v", err)
	}

	events := collectAgentTestEvents(t, agent.Run(ctx, &adk.AgentInput{
		Messages: []adk.Message{schema.UserMessage("run echo")},
	}))

	if echo.calls != 1 {
		t.Fatalf("stream tool calls = %d, want 1", echo.calls)
	}
	if final := lastAssistantContent(t, events); final != "stream result" {
		t.Fatalf("final output = %q, want stream result", final)
	}
}

func TestChatModelAgentAppliesStreamableToolCallMiddlewares(t *testing.T) {
	ctx := context.Background()
	echo := &streamTool{name: "echo", chunks: []string{"raw"}}
	agent, err := newChatModelAgent(ctx, chatModelAgentConfig{
		Name:          "chat",
		Description:   "test agent",
		Model:         &scriptedModel{responses: []*schema.Message{toolCallMessage("echo")}},
		MaxIterations: 3,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{echo},
				ToolCallMiddlewares: []compose.ToolMiddleware{
					{
						Streamable: func(endpoint compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
							return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
								if _, err := endpoint(ctx, input); err != nil {
									return nil, err
								}
								return &compose.StreamToolOutput{
									Result: schema.StreamReaderFromArray([]string{"wrapped"}),
								}, nil
							}
						},
					},
				},
			},
			ReturnDirectly: map[string]bool{"echo": true},
		},
	})
	if err != nil {
		t.Fatalf("newChatModelAgent() error = %v", err)
	}

	events := collectAgentTestEvents(t, agent.Run(ctx, &adk.AgentInput{
		Messages: []adk.Message{schema.UserMessage("run echo")},
	}))

	if echo.calls != 1 {
		t.Fatalf("stream tool calls = %d, want 1", echo.calls)
	}
	if final := lastAssistantContent(t, events); final != "wrapped" {
		t.Fatalf("final output = %q, want wrapped", final)
	}
}

func collectAgentTestEvents(t *testing.T, iter *adk.AsyncIterator[*adk.AgentEvent]) []*adk.AgentEvent {
	t.Helper()
	var events []*adk.AgentEvent
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event == nil {
			continue
		}
		if event.Err != nil {
			t.Fatalf("agent event error = %v", event.Err)
		}
		events = append(events, event)
	}
	return events
}

func lastAssistantContent(t *testing.T, events []*adk.AgentEvent) string {
	t.Helper()
	var final string
	for _, event := range events {
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		msg, err := event.Output.MessageOutput.GetMessage()
		if err != nil {
			t.Fatalf("GetMessage() error = %v", err)
		}
		if msg != nil && msg.Role == schema.Assistant && len(msg.ToolCalls) == 0 {
			final = msg.Content
		}
	}
	return final
}

func toolCallMessage(name string) *schema.Message {
	return schema.AssistantMessage("", []schema.ToolCall{{
		ID: "call-1",
		Function: schema.FunctionCall{
			Name:      name,
			Arguments: `{"value":"hello"}`,
		},
	}})
}

func clarificationMessage(question string) *schema.Message {
	return schema.AssistantMessage("", []schema.ToolCall{{
		ID: "clarify-1",
		Function: schema.FunctionCall{
			Name:      middlewares.AskClarificationToolName,
			Arguments: fmt.Sprintf(`{"question":%q}`, question),
		},
	}})
}
