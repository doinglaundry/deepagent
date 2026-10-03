package execution

import (
	"context"
	"errors"
	"fmt"

	"eino-cli/deepagent/graph/middleware"
	"eino-cli/deepagent/graph/tools"
	"eino-cli/deepagent/graph/types"
	"github.com/cloudwego/eino/components/tool"
)

// configure 将配置装配为当前 Graph 使用的工具、模型和中间件，不构图、不调用模型。
// 文件系统归 Thread 管理；装配失败时，这里只清理中间件资源。
func (a *Graph) configure(ctx context.Context) (err error) {
	// 使用配置副本补齐本次 RunID，避免修改原配置。
	cfg := *a.cfg.Clone()
	cfg.RunID = a.runID
	// 有 RunFactory 的中间件创建本次实例，避免多个 Run 共用可变状态。
	callers, err := a.newRunMiddlewares(ctx)
	if err != nil {
		return err
	}
	middlewares := make([]middleware.Middleware, 0, len(callers)+2)
	// 后续装配失败时，即使上游 context 已取消，也尝试关闭已创建的中间件。
	defer func() {
		if err != nil {
			err = errors.Join(err, closeMiddlewareResources(context.WithoutCancel(ctx), callers))
		}
	}()

	// 汇总显式工具，以及 Skills、文件系统、Web、中间件和子代理提供的工具。
	descriptors := append([]tools.ToolDescriptor(nil), cfg.ToolDescriptors...)
	if cfg.SkillLoader != nil {
		skill := middleware.NewSkillMiddleware(cfg.SkillLoader)
		middlewares = append(middlewares, skill)
		items, toolsErr := skill.Tools(ctx)
		if toolsErr != nil {
			return toolsErr
		}
		descriptors = appendToolDescriptors(descriptors, items)
	}
	filesystemConfig := cfg.FilesystemConfig
	if filesystemConfig != nil && cfg.Filesystem != nil {
		// 同时接入文件工具的使用说明，让模型知道如何操作当前文件系统。
		middlewares = append(middlewares, middleware.NewBasePromptMiddleware(tools.FilesystemPrompt))
		items, toolsErr := tools.NewFilesystemTools(cfg.Filesystem, tools.FilesystemToolOptions{
			ReadOnly:       filesystemConfig.ReadOnly,
			EnableCommands: !filesystemConfig.DisableExecute,
			EnablePatch:    !filesystemConfig.DisableApplyPatch,
			CommandTimeout: filesystemConfig.CommandTimeout,
		})
		if toolsErr != nil {
			return toolsErr
		}
		descriptors = appendToolDescriptors(descriptors, items)
	}
	if cfg.WebConfig != nil {
		items, toolsErr := tools.NewWebTools(ctx, cfg.WebConfig)
		if toolsErr != nil {
			return toolsErr
		}
		descriptors = appendToolDescriptors(descriptors, items)
	}
	middlewares = append(middlewares, callers...)
	for _, mw := range callers {
		items, toolsErr := mw.Tools(ctx)
		if toolsErr != nil {
			return toolsErr
		}
		descriptors = appendToolDescriptors(descriptors, items)
	}
	// 注册 task 工具作为子代理入口；真正调用 task 时才创建并执行子 Graph。
	if len(cfg.SubAgents) > 0 {
		names := make([]string, 0, len(cfg.SubAgents))
		for _, spec := range cfg.SubAgents {
			names = append(names, spec.Name)
		}
		task := tools.NewStreamingTaskTool(NewChildRunner(cfg), names...)
		descriptors = append(descriptors, tools.ToolDescriptor{
			Tool: task, ParallelSafe: true, ReadOnly: cfg.ReadOnlyToolsOnly,
		})
	}
	// 检查工具名称与重复定义、缓存 schema，再按只读限制和 ToolMask 过滤。
	toolSet, err := tools.NewToolSet(ctx, descriptors)
	if err != nil {
		return err
	}
	toolSet, err = toolSet.Filter(ctx, a.cfg.ReadOnlyToolsOnly, a.cfg.ToolMask)
	if err != nil {
		return err
	}
	infos, err := toolSet.ModelTools(ctx)
	if err != nil {
		return err
	}
	// 把最终工具 schema 绑定到模型；这里只告知模型有哪些工具，不执行工具。
	a.model = a.cfg.Model
	if len(infos) > 0 {
		a.model, err = a.cfg.Model.WithTools(infos)
		if err != nil {
			return err
		}
	}
	// 注册中间件状态供 checkpoint 保存与恢复，并判断是否允许提前执行工具。
	graphState, err := a.buildRuntimeState(middlewares)
	if err != nil {
		return err
	}
	a.middlewares, a.tools, a.graphState = middlewares, toolSet, graphState
	a.eager = a.canExecuteToolsEagerly(middlewares)
	a.resourcesOpen = true
	return nil
}

func appendToolDescriptors(descriptors []tools.ToolDescriptor, items []tool.BaseTool) []tools.ToolDescriptor {
	for _, item := range items {
		descriptors = append(descriptors, tools.Describe(item))
	}
	return descriptors
}

// newRunMiddlewares creates fresh mutable instances before any Tools method is called.
func (a *Graph) newRunMiddlewares(ctx context.Context) (middlewares []middleware.Middleware, err error) {
	defer func() {
		if err != nil {
			err = errors.Join(err, closeMiddlewareResources(context.WithoutCancel(ctx), middlewares))
		}
	}()
	for _, source := range a.cfg.Middlewares {
		if source == nil {
			continue
		}
		instance := source
		factory, ok := source.(middleware.RunFactory)
		if ok {
			instance = factory.NewRun()
		}
		if instance == nil {
			return middlewares, fmt.Errorf("middleware factory returned nil")
		}
		middlewares = append(middlewares, instance)
	}
	middlewares = append(middlewares, middleware.NewPatchToolCalls())
	return middlewares, nil
}

func (a *Graph) canExecuteToolsEagerly(middlewares []middleware.Middleware) bool {
	if !a.cfg.EnableEagerTools {
		return false
	}
	for _, mw := range middlewares {
		guard, ok := mw.(interface{ RequiresCompleteModelResponse() bool })
		if ok && guard.RequiresCompleteModelResponse() {
			return false
		}
	}
	return true
}

func (a *Graph) buildRuntimeState(middlewares []middleware.Middleware) (*types.GraphState, error) {
	state := types.NewGraphState()
	for _, mw := range middlewares {
		handler := mw.BuildStateHandler()
		if handler != nil {
			name := mw.Name()
			_, exists := state.StateHolder[name]
			if exists {
				return nil, fmt.Errorf("duplicate stateful middleware name %q", name)
			}
			state.RegisterStateful(name, handler)
		}
	}

	return state, nil
}
