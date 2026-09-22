package deepagents

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"eino-cli/deepagent/core/constant"
	graph_lib "eino-cli/deepagent/core/graph"
	"eino-cli/deepagent/core/middlewares"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	serialiser "eino-cli/deepagent/helper/serialiser"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// buildGraphWithConfig 构建并编译模型、工具及继续执行节点组成的 Eino 图。
//
// 参数:
//   - ctx: 图构建上下文。
//   - cfg: 模型、路由和 checkpoint 等配置。
//   - allTools: 已收集并包装的工具。
//   - chain: 节点前后执行的中间件链。
//
// 返回值:
//   - runnable: 编译后的可执行图。
//   - err: 工具绑定、节点、连边或编译失败。
//
// 功能特点:
//  1. 图从模型开始；工具结果回到模型，继续节点支持追加输入处理。
//
// 执行流程:
//  1. 绑定工具到模型并初始化图状态。
//  2. 添加模型、工具和可选 continue 节点。
//  3. 连接分支，设置执行步数、checkpoint 与中断选项后编译。
//
// 使用示例:
//   - DeepAgent.New 在构造阶段调用，真正执行由 Stream 或 Run 触发。
func buildGraphWithConfig(
	ctx context.Context,
	cfg Config,
	allTools []tool.BaseTool,
	chain *middleware.MiddlewareChain,
) (runnable compose.Runnable[[]*schema.Message, *schema.Message], err error) {
	// 1. 向模型绑定本轮可调用的工具定义。
	modelWithTools, err := bindToolsToModel(ctx, cfg.Model, allTools)
	if err != nil {
		return nil, err
	}

	// 2. 为每次图执行创建独立状态，并注册模型和工具节点。
	graph := compose.NewGraph[[]*schema.Message, *schema.Message](
		compose.WithGenLocalState(func(_ context.Context) (state *types.GraphLocalState) {
			return &types.GraphLocalState{}
		}))

	var modelPostHandler compose.GraphAddNodeOpt
	if cfg.EnableStreamToolCall {
		modelPostHandler = compose.WithStreamStatePostHandler(createModelStreamPostHandler(chain))
	} else {
		modelPostHandler = compose.WithStatePostHandler(createModelPostHandler(chain))
	}
	err = graph.AddChatModelNode(constant.NodeKeyModel, modelWithTools,
		compose.WithStatePreHandler(createModelPreHandler(chain)),
		modelPostHandler,
		compose.WithNodeName(constant.NodeKeyModel),
	)
	if err != nil {
		return nil, err
	}

	err = addToolsNode(ctx, graph, &cfg, allTools, chain)
	if err != nil {
		return nil, err
	}

	err = addContinueNode(graph, cfg.ContinueAfterModel != nil)
	if err != nil {
		return nil, err
	}

	// 3. 模型选择工具、继续或结束；工具执行后再次回到模型。
	if err := graph.AddEdge(compose.START, constant.NodeKeyModel); err != nil {
		return nil, err
	}
	if cfg.ContinueAfterModel != nil {
		if err := graph.AddEdge(constant.NodeKeyContinue, constant.NodeKeyModel); err != nil {
			return nil, err
		}
	}
	if err := connectModelRoute(graph, &cfg, len(allTools) > 0); err != nil {
		return nil, err
	}
	if len(allTools) > 0 {
		if err := graph.AddEdge(constant.NodeKeyTools, constant.NodeKeyModel); err != nil {
			return nil, err
		}
	}

	// 4. 编译时绑定执行上限与恢复配置，此处尚未调用模型。
	compileOpts := []compose.GraphCompileOption{
		compose.WithGraphName(constant.GraphName),
		compose.WithNodeTriggerMode(compose.AnyPredecessor),
		compose.WithMaxRunSteps(cfg.MaxSteps),
	}
	if cfg.CheckpointStore != nil {
		compileOpts = append(compileOpts, compose.WithCheckPointStore(cfg.CheckpointStore))
	}
	if len(cfg.InterruptBeforeNodes) > 0 {
		compileOpts = append(compileOpts, compose.WithInterruptBeforeNodes(cfg.InterruptBeforeNodes))
	}
	if len(cfg.InterruptAfterNodes) > 0 {
		compileOpts = append(compileOpts, compose.WithInterruptAfterNodes(cfg.InterruptAfterNodes))
	}
	return graph.Compile(ctx, compileOpts...)
}

func bindToolsToModel(ctx context.Context, chatModel model.ToolCallingChatModel, allTools []tool.BaseTool) (boundModel model.ToolCallingChatModel, err error) {
	if len(allTools) == 0 {
		return chatModel, nil
	}

	toolInfos := make([]*schema.ToolInfo, 0, len(allTools))
	for _, t := range allTools {
		info, err := t.Info(ctx)
		if err != nil {
			return nil, err
		}
		toolInfos = append(toolInfos, info)
	}

	return chatModel.WithTools(toolInfos)
}

func addToolsNode(
	ctx context.Context,
	graph *compose.Graph[[]*schema.Message, *schema.Message],
	cfg *Config,
	allTools []tool.BaseTool,
	chain *middleware.MiddlewareChain,
) (err error) {
	if len(allTools) == 0 {
		return nil
	}

	toolsConfig := compose.ToolsNodeConfig{
		Tools:               allTools,
		UnknownToolsHandler: unknownToolHandler(toolNames(ctx, allTools)),
		ExecuteSequentially: false,
		ToolCallMiddlewares: chain.ToolCallMiddlewares(),
	}
	if cfg.EnableStreamToolCall {
		streamingToolsNode, err := graph_lib.CreateStreamingToolLambda(ctx, &toolsConfig)
		if err != nil {
			return err
		}
		return graph.AddLambdaNode(constant.NodeKeyTools, streamingToolsNode,
			compose.WithNodeName(constant.NodeKeyTools),
		)
	}

	toolsNode, err := compose.NewToolNode(ctx, &toolsConfig)
	if err != nil {
		return err
	}
	toolNodeOpts := []compose.GraphAddNodeOpt{
		compose.WithNodeName(constant.NodeKeyTools),
	}
	if cfg.ToolNodePreHandler != nil {
		toolNodeOpts = append(toolNodeOpts, compose.WithStatePreHandler(adaptToolNodePreHandler(cfg.ToolNodePreHandler)))
	}
	if cfg.ToolNodePostHandler != nil {
		toolNodeOpts = append(toolNodeOpts, compose.WithStatePostHandler(adaptToolNodePostHandler(cfg.ToolNodePostHandler)))
	}
	return graph.AddToolsNode(constant.NodeKeyTools, toolsNode, toolNodeOpts...)
}

func addContinueNode(
	graph *compose.Graph[[]*schema.Message, *schema.Message],
	enabled bool,
) (err error) {
	if !enabled {
		return nil
	}

	continueNode, err := compose.AnyLambda[*schema.Message, []*schema.Message, struct{}](
		func(context.Context, *schema.Message, ...struct{}) ([]*schema.Message, error) {
			return []*schema.Message{}, nil
		},
		nil,
		func(_ context.Context, input *schema.StreamReader[*schema.Message], _ ...struct{}) ([]*schema.Message, error) {
			if input != nil {
				defer input.Close()
				for {
					if _, err := input.Recv(); err != nil {
						break
					}
				}
			}
			return []*schema.Message{}, nil
		},
		nil,
	)
	if err != nil {
		return err
	}
	return graph.AddLambdaNode(constant.NodeKeyContinue, continueNode,
		compose.WithNodeName(constant.NodeKeyContinue),
	)
}

func connectModelRoute(
	graph *compose.Graph[[]*schema.Message, *schema.Message],
	cfg *Config,
	hasTools bool,
) (err error) {
	if !hasTools && cfg.ContinueAfterModel == nil {
		return graph.AddEdge(constant.NodeKeyModel, compose.END)
	}

	endNodes := map[string]bool{compose.END: true}
	if hasTools {
		endNodes[constant.NodeKeyTools] = true
	}
	if cfg.ContinueAfterModel != nil {
		endNodes[constant.NodeKeyContinue] = true
	}

	selectNextNode := func(ctx context.Context, hasToolCall bool) (nextNode string, err error) {
		if hasTools && hasToolCall {
			return constant.NodeKeyTools, nil
		}
		if cfg.ContinueAfterModel == nil {
			return compose.END, nil
		}
		continueRun, err := cfg.ContinueAfterModel(ctx)
		if err != nil {
			return "", err
		}
		if continueRun {
			return constant.NodeKeyContinue, nil
		}
		return compose.END, nil
	}

	if !cfg.EnableStreamToolCall {
		return graph.AddBranch(constant.NodeKeyModel, compose.NewGraphBranch(
			func(ctx context.Context, message *schema.Message) (string, error) {
				hasToolCall := message != nil && len(message.ToolCalls) > 0
				return selectNextNode(ctx, hasToolCall)
			},
			endNodes,
		))
	}

	return graph.AddBranch(constant.NodeKeyModel, compose.NewStreamGraphBranch(
		func(ctx context.Context, input *schema.StreamReader[*schema.Message]) (string, error) {
			hasToolCall, err := graph_lib.StreamHasToolCall(ctx, input)
			if err != nil {
				return "", err
			}
			return selectNextNode(ctx, hasToolCall)
		},
		endNodes,
	))
}

func toolNames(ctx context.Context, tools []tool.BaseTool) []string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		info, err := t.Info(ctx)
		if err != nil || info == nil || info.Name == "" {
			continue
		}
		names = append(names, info.Name)
	}
	return names
}

func unknownToolHandler(available []string) func(ctx context.Context, name, input string) (string, error) {
	return func(ctx context.Context, name, input string) (string, error) {
		if len(available) == 0 {
			return fmt.Sprintf("Tool %q is not available in this runtime. Continue without this tool or answer with the available context.", name), nil
		}
		return fmt.Sprintf("Tool %q is not available in this runtime. Use one of the available tools instead: %s.", name, strings.Join(available, ", ")), nil
	}
}

// createModelPreHandler creates the PreHandler for the Executor node.
//
// Flow:
//  1. Accumulate input messages into graph state
//  2. chain.ModifyModelRequest — pipeline: each middleware transforms messages
//     (including BasePromptMiddleware which prepends the system prompt)
func createModelPreHandler(
	chain *middleware.MiddlewareChain,
) compose.StatePreHandler[[]*schema.Message, *types.GraphLocalState] {
	return func(ctx context.Context, messages []*schema.Message, _ *types.GraphLocalState) ([]*schema.Message, error) {
		if chain == nil {
			return messages, nil
		}

		prompts, err := chain.BuildPrompts(ctx)
		if err != nil {
			return nil, fmt.Errorf("middleware BuildPrompts failed: %w", err)
		}

		state := types.StateFromContext(ctx)
		messages, err = chain.ModifyModelRequest(ctx, prompts, messages, state)
		if err != nil {
			return nil, fmt.Errorf("middleware ModifyModelRequest failed: %w", err)
		}

		return messages, nil
	}
}

func createModelPostHandler(chain *middleware.MiddlewareChain) compose.StatePostHandler[*schema.Message, *types.GraphLocalState] {
	return func(ctx context.Context, message *schema.Message, _ *types.GraphLocalState) (*schema.Message, error) {
		if chain == nil {
			return message, nil
		}

		state := types.StateFromContext(ctx)
		message, err := chain.ModifyModelResponse(ctx, message, state)
		if err != nil {
			return nil, fmt.Errorf("middleware ModifyModelResponse failed: %w", err)
		}
		return message, nil
	}
}

func createModelStreamPostHandler(chain *middleware.MiddlewareChain) compose.StreamStatePostHandler[*schema.Message, *types.GraphLocalState] {
	return func(ctx context.Context, out *schema.StreamReader[*schema.Message], _ *types.GraphLocalState) (*schema.StreamReader[*schema.Message], error) {
		if chain == nil {
			return out, nil
		}

		state := types.StateFromContext(ctx)
		out, err := chain.ModifyModelStreamResponse(ctx, out, state)
		if err != nil {
			return nil, fmt.Errorf("middleware ModifyModelStreamResponse failed: %w", err)
		}
		return out, nil
	}
}

func adaptToolNodePreHandler(handler ToolNodePreHandler) compose.StatePreHandler[*schema.Message, *types.GraphLocalState] {
	return func(ctx context.Context, input *schema.Message, _ *types.GraphLocalState) (*schema.Message, error) {
		return handler(ctx, input)
	}
}

func adaptToolNodePostHandler(handler ToolNodePostHandler) compose.StatePostHandler[[]*schema.Message, *types.GraphLocalState] {
	return func(ctx context.Context, output []*schema.Message, _ *types.GraphLocalState) ([]*schema.Message, error) {
		return handler(ctx, output)
	}
}

func buildAgentState(ctx context.Context, chain *middleware.MiddlewareChain, customGraphState map[string]types.RunTimeStateful, store compose.CheckPointStore) (*types.GraphState, error) {
	agentState := types.NewGraphState(store)
	if chain != nil {
		// 为每个 middleware 构建状态处理器
		handlers := chain.BuildStateHandlers()

		if len(handlers) != 0 && store == nil {
			needCheckpointStoreMiddlewares := strings.Join(slices.Sorted(maps.Keys(handlers)), ",")

			slog.ErrorContext(ctx, fmt.Sprintf("[DeepAgent::buildAgentState] check point store is required by %s", needCheckpointStoreMiddlewares))
			return nil, fmt.Errorf("check point store is required by %s", needCheckpointStoreMiddlewares)
		}

		for k, h := range handlers {
			agentState.RegisterStateful(k, h)
		}
	}
	// 注册自定义状态ful
	for k, v := range customGraphState {
		agentState.RegisterStateful(k, v)
	}
	return agentState, nil
}

func collectAllTools(ctx context.Context, chain *middleware.MiddlewareChain, cfg *Config) ([]tool.BaseTool, error) {
	// 从中间件收集工具
	middlewareTools, err := chain.Tools(ctx)
	if err != nil {
		return nil, err
	}
	userInjectTools := cfg.Tools
	allTools := make([]tool.BaseTool, 0, 32)
	allTools = append(allTools, middlewareTools...)
	allTools = append(allTools, userInjectTools...)

	hitl := cfg.HITLConfig
	if hitl != nil {
		if hitl.NeedFollowUpTool {
			allTools = append(allTools, tools.GetFollowUpTool())
		}
	}
	if cfg.ReadOnlyToolsOnly {
		allTools = slices.DeleteFunc(allTools, func(t tool.BaseTool) bool {
			readOnly, ok := t.(interface{ ReadOnly() bool })
			return !ok || !readOnly.ReadOnly()
		})
	}

	allTools = filterToolsByMask(ctx, allTools, cfg.ToolMask)

	if hitl != nil {
		for i, t := range allTools {
			tInfo, err := t.Info(ctx)
			if err != nil {
				slog.ErrorContext(ctx, fmt.Sprintf("[DeepAgent::collectAllTools] get tool info failed,  err: %v", err))
				continue
			}

			if gate, exists := hitl.ToolPolicyGates[tInfo.Name]; exists {
				if invokeTool, ok := t.(tool.InvokableTool); ok {
					if gate.Policy == nil {
						return nil, fmt.Errorf("tool %q policy gate requires Policy", tInfo.Name)
					}
					allTools[i] = tools.NewInvokablePolicyTool(invokeTool, gate)
				}

				continue
			}

		}
	}

	// 包装所有工具。流式工具参数在收集器中只修复明确无歧义的 JSON。
	allTools = tools.WrapToolsWithConfig(allTools, &tools.WrapToolsConfig{
		InfoRewriter: cfg.ToolInfoRewriter,
	})
	// 后置调用一下打印下日志
	var infos []*schema.ToolInfo
	for _, t := range allTools {
		info, _ := t.Info(ctx)
		infos = append(infos, info)
	}
	slog.InfoContext(ctx, fmt.Sprintf("[DeepAgent::collectAllTools] allToolsInfo: %v", serialiser.ToString(infos)))
	return allTools, nil
}

func filterToolsByMask(ctx context.Context, toolList []tool.BaseTool, mask tools.Mask) []tool.BaseTool {
	if mask == nil || len(toolList) == 0 {
		return toolList
	}

	filtered := make([]tool.BaseTool, 0, len(toolList))
	for _, t := range toolList {
		info, err := t.Info(ctx)
		if err != nil {
			slog.ErrorContext(ctx, fmt.Sprintf("[DeepAgent::filterToolsByMask] get tool info failed, keep tool by default, err: %v", err))
			filtered = append(filtered, t)
			continue
		}
		if info == nil {
			slog.WarnContext(ctx, "[DeepAgent::filterToolsByMask] tool info is nil, keep tool by default")
			filtered = append(filtered, t)
			continue
		}
		if mask(ctx, info) {
			filtered = append(filtered, t)
			continue
		}
		slog.InfoContext(ctx, fmt.Sprintf("[DeepAgent::filterToolsByMask] masked tool: %s", info.Name))
	}

	return filtered
}
