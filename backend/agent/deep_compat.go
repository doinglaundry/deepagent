package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

type deepCompatibleAgentConfig struct {
	Name                string
	Description         string
	ChatModel           model.BaseChatModel
	Instruction         string
	MaxIterations       int
	EnableWriteTodos    bool
	EnableGeneralTask   bool
	Handlers            []adk.ChatModelAgentMiddleware
	ToolsConfig         adk.ToolsConfig
	ModelRetryConfig    *adk.ModelRetryConfig
	ModelFailoverConfig *adk.ModelFailoverConfig
	OutputKey           string
}

type writeTodosArguments struct {
	Todos []deep.TODO `json:"todos"`
}

type appendToolMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	tool        tool.BaseTool
	instruction string
}

type taskToolArguments struct {
	SubagentType string `json:"subagent_type"`
	Description  string `json:"description"`
}

type taskTool struct {
	subagents []adk.Agent
	tools     map[string]tool.InvokableTool
}

func newDeepCompatibleAgent(ctx context.Context, cfg deepCompatibleAgentConfig) (adk.ResumableAgent, error) {
	if cfg.Name == "" {
		return nil, fmt.Errorf("agent name is required")
	}
	if cfg.ChatModel == nil {
		return nil, fmt.Errorf("chat model is required")
	}

	handlers, err := buildDeepCompatibleHandlers(ctx, cfg)
	if err != nil {
		return nil, err
	}

	agentImpl, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:                cfg.Name,
		Description:         cfg.Description,
		Instruction:         cfg.Instruction,
		Model:               cfg.ChatModel,
		ToolsConfig:         cfg.ToolsConfig,
		GenModelInput:       genModelInput,
		MaxIterations:       cfg.MaxIterations,
		Handlers:            handlers,
		ModelRetryConfig:    cfg.ModelRetryConfig,
		ModelFailoverConfig: cfg.ModelFailoverConfig,
		OutputKey:           cfg.OutputKey,
	})
	if err != nil {
		return nil, fmt.Errorf("build chat model agent: %w", err)
	}
	return agentImpl, nil
}

func buildDeepCompatibleHandlers(
	ctx context.Context,
	cfg deepCompatibleAgentConfig,
) ([]adk.ChatModelAgentMiddleware, error) {
	userHandlers := append([]adk.ChatModelAgentMiddleware(nil), cfg.Handlers...)
	builtinHandlers := make([]adk.ChatModelAgentMiddleware, 0, 2)

	if cfg.EnableWriteTodos {
		writeTodos, err := newWriteTodosHandler()
		if err != nil {
			return nil, fmt.Errorf("build write_todos handler: %w", err)
		}
		builtinHandlers = append(builtinHandlers, writeTodos)
	}
	if cfg.EnableGeneralTask {
		subagentHandlers := append(append([]adk.ChatModelAgentMiddleware(nil), builtinHandlers...), userHandlers...)
		taskHandler, err := newTaskToolHandler(ctx, cfg, subagentHandlers)
		if err != nil {
			return nil, fmt.Errorf("build task handler: %w", err)
		}
		builtinHandlers = append(builtinHandlers, taskHandler)
	}
	return append(append([]adk.ChatModelAgentMiddleware(nil), builtinHandlers...), userHandlers...), nil
}

func genModelInput(_ context.Context, instruction string, input *adk.AgentInput) ([]adk.Message, error) {
	messages := make([]adk.Message, 0, len(input.Messages)+1)
	if instruction != "" {
		messages = append(messages, schema.SystemMessage(instruction))
	}
	messages = append(messages, input.Messages...)
	return messages, nil
}

func newWriteTodosHandler() (adk.ChatModelAgentMiddleware, error) {
	writeTodos, err := utils.InferTool(
		"write_todos",
		writeTodosDescription,
		func(ctx context.Context, input writeTodosArguments) (string, error) {
			adk.AddSessionValue(ctx, deep.SessionKeyTodos, input.Todos)
			todos, err := sonic.MarshalString(input.Todos)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Updated todo list to %s", todos), nil
		},
	)
	if err != nil {
		return nil, err
	}
	return &appendToolMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		tool:                         writeTodos,
	}, nil
}

func (m *appendToolMiddleware) BeforeAgent(
	ctx context.Context,
	runCtx *adk.ChatModelAgentContext,
) (context.Context, *adk.ChatModelAgentContext, error) {
	if runCtx == nil || m.tool == nil {
		return ctx, runCtx, nil
	}
	next := *runCtx
	next.Instruction += m.instruction
	next.Tools = append(append([]tool.BaseTool(nil), runCtx.Tools...), m.tool)
	return ctx, &next, nil
}

func newTaskToolHandler(
	ctx context.Context,
	cfg deepCompatibleAgentConfig,
	parentHandlers []adk.ChatModelAgentMiddleware,
) (adk.ChatModelAgentMiddleware, error) {
	generalAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:                "general-purpose",
		Description:         "general-purpose agent for researching complex questions, searching for code, and executing multi-step tasks. (Tools: *)",
		Instruction:         cfg.Instruction,
		Model:               cfg.ChatModel,
		ToolsConfig:         cfg.ToolsConfig,
		GenModelInput:       genModelInput,
		MaxIterations:       cfg.MaxIterations,
		Handlers:            parentHandlers,
		ModelRetryConfig:    cfg.ModelRetryConfig,
		ModelFailoverConfig: cfg.ModelFailoverConfig,
	})
	if err != nil {
		return nil, err
	}

	agentTool, ok := adk.NewAgentTool(ctx, generalAgent).(tool.InvokableTool)
	if !ok {
		return nil, fmt.Errorf("general-purpose agent tool is not invokable")
	}

	return &appendToolMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		tool: &taskTool{
			subagents: []adk.Agent{generalAgent},
			tools: map[string]tool.InvokableTool{
				generalAgent.Name(ctx): agentTool,
			},
		},
		instruction: taskInstruction,
	}, nil
}

func (t *taskTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	var descriptions strings.Builder
	for _, agent := range t.subagents {
		descriptions.WriteString(fmt.Sprintf("- %s: %s\n", agent.Name(ctx), agent.Description(ctx)))
	}
	return &schema.ToolInfo{
		Name: "task",
		Desc: fmt.Sprintf(taskToolDescription, descriptions.String()),
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"subagent_type": {
				Type: schema.String,
			},
			"description": {
				Type: schema.String,
			},
		}),
	}, nil
}

func (t *taskTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	input := &taskToolArguments{}
	if err := json.Unmarshal([]byte(argumentsInJSON), input); err != nil {
		return "", fmt.Errorf("decode task tool arguments: %w", err)
	}

	subagent, ok := t.tools[input.SubagentType]
	if !ok {
		return "", fmt.Errorf("subagent type %s not found", input.SubagentType)
	}

	params, err := sonic.MarshalString(map[string]string{
		"request": input.Description,
	})
	if err != nil {
		return "", err
	}
	return subagent.InvokableRun(ctx, params, opts...)
}

const writeTodosDescription = `Create and update the structured todo list for the current coding session.

Use this tool for complex or multi-step implementation work so progress can be
tracked by the UI. Keep exactly one item in_progress unless independent work is
actually running in parallel. Mark items completed immediately after finishing
them, and remove items that are no longer relevant.

Skip this tool for trivial one-step requests or purely conversational answers.`

const taskInstruction = `

# 'task' subagent tool

Use the task tool to launch a short-lived subagent for complex, independent,
multi-step work. The subagent handles the delegated request autonomously and
returns a single final result. Prefer direct tool calls for small or tightly
coupled work where the intermediate results need to stay visible in this turn.
`

const taskToolDescription = `Launch a subagent to handle complex, multi-step work autonomously.

Available subagents:
%s
Arguments:
- subagent_type: the exact subagent name from the list above.
- description: a clear, self-contained task request for the subagent.`
