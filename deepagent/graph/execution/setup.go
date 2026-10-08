package execution

import (
	"context"
	"errors"
	"fmt"

	"eino-cli/deepagent/graph/middleware"
	"eino-cli/deepagent/graph/tools"
	"eino-cli/deepagent/graph/types"
	messagepkg "eino-cli/deepagent/message"
)

// configure 将配置装配为当前 Graph 使用的工具、模型和中间件，不构图、不调用模型。
// 文件系统归 Thread 管理；装配失败时，这里只清理中间件资源。
func (graph *Graph) configure(ctx context.Context) (err error) {
	// 使用配置副本补齐本次 RunID，避免修改原配置。
	config := *graph.config.Clone()
	config.RunID = graph.runID
	// 中间件只处理提示和执行钩子，不注册工具。
	middlewares, err := graph.newRunMiddlewares(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, closeMiddlewareResources(context.WithoutCancel(ctx), middlewares))
		}
	}()

	// 工具单独装配、校验和过滤，再将 schema 绑定到模型。
	toolSet, err := newToolSet(ctx, config)
	if err != nil {
		return err
	}
	toolInfos, err := toolSet.GetToolInfos(ctx)
	if err != nil {
		return err
	}
	// 把最终工具 schema 绑定到模型；这里只告知模型有哪些工具，不执行工具。
	graph.chatModel = config.Model
	if len(toolInfos) > 0 {
		graph.chatModel, err = config.Model.WithTools(toolInfos)
		if err != nil {
			return err
		}
	}
	// 注册中间件状态供 checkpoint 保存与恢复，并判断是否允许提前执行工具。
	graphState, err := graph.buildRuntimeState(middlewares)
	if err != nil {
		return err
	}
	// 固定说明属于配置；动态提示仍由中间件在每次模型请求前生成。
	if config.FilesystemConfig != nil && config.Filesystem != nil {
		config.Prompts = append(config.Prompts, messagepkg.NewSystemMessage(tools.FilesystemPrompt))
	}
	graph.config.Prompts = config.Prompts
	graph.middlewares, graph.toolSet, graph.graphState = middlewares, toolSet, graphState
	graph.enableEagerTools = graph.canExecuteToolsEagerly(middlewares)
	graph.resourcesOpen = true
	return nil
}

// newToolSet 只装配工具，不访问中间件。
func newToolSet(ctx context.Context, config Config) (*tools.ToolSet, error) {
	toolDescriptors := append([]tools.ToolDescriptor(nil), config.ToolDescriptors...)
	if config.SkillLoader != nil {
		toolDescriptors = append(toolDescriptors, tools.NewActivateSkillTool(config.SkillLoader))
	}
	filesystemConfig := config.FilesystemConfig
	if filesystemConfig != nil && config.Filesystem != nil {
		featureToolDescriptors, err := tools.NewFilesystemTools(config.Filesystem, tools.FilesystemToolOptions{
			ReadOnly:       filesystemConfig.ReadOnly,
			EnableCommands: !filesystemConfig.DisableExecute,
			EnablePatch:    !filesystemConfig.DisableApplyPatch,
			CommandTimeout: filesystemConfig.CommandTimeout,
		})
		if err != nil {
			return nil, err
		}
		toolDescriptors = append(toolDescriptors, featureToolDescriptors...)
	}
	if config.WebConfig != nil {
		featureToolDescriptors, err := tools.NewWebTools(ctx, config.WebConfig)
		if err != nil {
			return nil, err
		}
		toolDescriptors = append(toolDescriptors, featureToolDescriptors...)
	}
	if len(config.SubAgents) > 0 {
		subAgentNames := make([]string, 0, len(config.SubAgents))
		for _, subAgent := range config.SubAgents {
			subAgentNames = append(subAgentNames, subAgent.Name)
		}
		toolDescriptors = append(toolDescriptors, tools.NewStreamingTaskTool(NewChildRunner(config), config.ReadOnlyToolsOnly, subAgentNames...))
	}
	toolSet, err := tools.NewToolSet(ctx, toolDescriptors)
	if err != nil {
		return nil, err
	}
	return toolSet.FilterTools(ctx, config.ReadOnlyToolsOnly, config.ToolMask)
}

// newRunMiddlewares creates dynamic prompts and fresh mutable instances for this Run.
func (graph *Graph) newRunMiddlewares(ctx context.Context) (middlewares []middleware.Middleware, err error) {
	defer func() {
		if err != nil {
			err = errors.Join(err, closeMiddlewareResources(context.WithoutCancel(ctx), middlewares))
		}
	}()
	if graph.config.SkillLoader != nil {
		middlewares = append(middlewares, middleware.NewSkillMiddleware(graph.config.SkillLoader))
	}
	for _, configuredMiddleware := range graph.config.Middlewares {
		if configuredMiddleware == nil {
			continue
		}
		runMiddleware := configuredMiddleware
		runFactory, ok := configuredMiddleware.(middleware.RunFactory)
		if ok {
			runMiddleware = runFactory.NewRun()
		}
		if runMiddleware == nil {
			return middlewares, fmt.Errorf("middleware factory returned nil")
		}
		middlewares = append(middlewares, runMiddleware)
	}
	middlewares = append(middlewares, middleware.NewPatchToolCalls())
	return middlewares, nil
}

func (graph *Graph) canExecuteToolsEagerly(middlewares []middleware.Middleware) bool {
	if !graph.config.EnableEagerTools {
		return false
	}
	for _, currentMiddleware := range middlewares {
		responseGuard, ok := currentMiddleware.(interface{ RequiresCompleteModelResponse() bool })
		if ok && responseGuard.RequiresCompleteModelResponse() {
			return false
		}
	}
	return true
}

func (graph *Graph) buildRuntimeState(middlewares []middleware.Middleware) (*types.GraphState, error) {
	graphState := types.NewGraphState()
	for _, currentMiddleware := range middlewares {
		stateHandler := currentMiddleware.GetStateHandler()
		if stateHandler != nil {
			middlewareName := currentMiddleware.GetName()
			_, exists := graphState.StateHolder[middlewareName]
			if exists {
				return nil, fmt.Errorf("duplicate stateful middleware name %q", middlewareName)
			}
			graphState.RegisterStateful(middlewareName, stateHandler)
		}
	}

	return graphState, nil
}
