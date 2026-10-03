package execution

import (
	"context"
	"errors"
	"fmt"

	"eino-cli/deepagent/graph/middleware"
	"eino-cli/deepagent/graph/tools"
	"eino-cli/deepagent/graph/types"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// configure 将配置装配为当前 Graph 使用的工具、模型和中间件，不构图、不调用模型。
// 文件系统归 Thread 管理；装配失败时，这里只清理中间件资源。
func (a *Graph) configure(ctx context.Context) (err error) {
	// 使用配置副本补齐本次 RunID，避免修改原配置。
	cfg := *a.cfg.Clone()
	cfg.RunID = a.runID
	// 中间件只处理提示和执行钩子，不注册工具。
	middlewares, err := a.newRunMiddlewares(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, closeMiddlewareResources(context.WithoutCancel(ctx), middlewares))
		}
	}()

	// 工具单独装配、校验和过滤，再将 schema 绑定到模型。
	toolSet, err := newToolSet(ctx, cfg)
	if err != nil {
		return err
	}
	infos, err := toolSet.ModelTools(ctx)
	if err != nil {
		return err
	}
	// 把最终工具 schema 绑定到模型；这里只告知模型有哪些工具，不执行工具。
	a.model = cfg.Model
	if len(infos) > 0 {
		a.model, err = cfg.Model.WithTools(infos)
		if err != nil {
			return err
		}
	}
	// 注册中间件状态供 checkpoint 保存与恢复，并判断是否允许提前执行工具。
	graphState, err := a.buildRuntimeState(middlewares)
	if err != nil {
		return err
	}
	// 固定说明属于配置；动态提示仍由中间件在每次模型请求前生成。
	if cfg.FilesystemConfig != nil && cfg.Filesystem != nil {
		cfg.Prompts = append(cfg.Prompts, schema.SystemMessage(tools.FilesystemPrompt))
	}
	a.cfg.Prompts = cfg.Prompts
	a.middlewares, a.tools, a.graphState = middlewares, toolSet, graphState
	a.eager = a.canExecuteToolsEagerly(middlewares)
	a.resourcesOpen = true
	return nil
}

// newToolSet 只装配工具，不访问中间件。
func newToolSet(ctx context.Context, cfg Config) (*tools.ToolSet, error) {
	descriptors := append([]tools.ToolDescriptor(nil), cfg.ToolDescriptors...)
	if cfg.SkillLoader != nil {
		descriptors = append(descriptors, tools.Describe(tools.NewActivateSkillTool(cfg.SkillLoader)))
	}
	filesystemConfig := cfg.FilesystemConfig
	if filesystemConfig != nil && cfg.Filesystem != nil {
		items, err := tools.NewFilesystemTools(cfg.Filesystem, tools.FilesystemToolOptions{
			ReadOnly:       filesystemConfig.ReadOnly,
			EnableCommands: !filesystemConfig.DisableExecute,
			EnablePatch:    !filesystemConfig.DisableApplyPatch,
			CommandTimeout: filesystemConfig.CommandTimeout,
		})
		if err != nil {
			return nil, err
		}
		descriptors = appendToolDescriptors(descriptors, items)
	}
	if cfg.WebConfig != nil {
		items, err := tools.NewWebTools(ctx, cfg.WebConfig)
		if err != nil {
			return nil, err
		}
		descriptors = appendToolDescriptors(descriptors, items)
	}
	if len(cfg.SubAgents) > 0 {
		names := make([]string, 0, len(cfg.SubAgents))
		for _, spec := range cfg.SubAgents {
			names = append(names, spec.Name)
		}
		descriptors = append(descriptors, tools.ToolDescriptor{
			Tool:         tools.NewStreamingTaskTool(NewChildRunner(cfg), names...),
			ParallelSafe: true, ReadOnly: cfg.ReadOnlyToolsOnly,
		})
	}
	toolSet, err := tools.NewToolSet(ctx, descriptors)
	if err != nil {
		return nil, err
	}
	return toolSet.Filter(ctx, cfg.ReadOnlyToolsOnly, cfg.ToolMask)
}

func appendToolDescriptors(descriptors []tools.ToolDescriptor, items []tool.BaseTool) []tools.ToolDescriptor {
	for _, item := range items {
		descriptors = append(descriptors, tools.Describe(item))
	}
	return descriptors
}

// newRunMiddlewares creates dynamic prompts and fresh mutable instances for this Run.
func (a *Graph) newRunMiddlewares(ctx context.Context) (middlewares []middleware.Middleware, err error) {
	defer func() {
		if err != nil {
			err = errors.Join(err, closeMiddlewareResources(context.WithoutCancel(ctx), middlewares))
		}
	}()
	if a.cfg.SkillLoader != nil {
		middlewares = append(middlewares, middleware.NewSkillMiddleware(a.cfg.SkillLoader))
	}
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
