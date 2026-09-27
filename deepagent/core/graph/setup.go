package graph

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/components/model"
)

// configureRun binds one run's middleware, tools, model and state handlers
// before compiling the graph with that run's checkpoint store.
func (a *DeepAgent) configureRun(ctx context.Context) (err error) {
	childConfig := *a.cfg.Clone()
	childConfig.RunID = a.runID

	middlewares, err := a.newRunMiddlewares(ctx, &childConfig)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, closeMiddlewareResources(context.WithoutCancel(ctx), middlewares))
		}
	}()

	descriptors, err := a.collectToolDescriptors(ctx, middlewares, childConfig)
	if err != nil {
		return err
	}
	registry, err := a.buildToolRegistry(ctx, descriptors)
	if err != nil {
		return err
	}
	chatModel, err := a.bindModelTools(ctx, registry)
	if err != nil {
		return err
	}

	a.middlewares = middlewares
	a.registry = registry
	a.model = chatModel
	a.policy = a.buildPolicy()
	a.eager = a.canExecuteToolsEagerly(middlewares)
	a.graphState = a.buildRuntimeState(middlewares)

	err = a.buildGraph(ctx)
	if err != nil {
		return err
	}
	a.resourcesOpen = true
	return nil
}

// newRunMiddlewares creates run-local middleware. The caller owns the filesystem.
func (a *DeepAgent) newRunMiddlewares(ctx context.Context, childConfig *Config) (middlewares []middleware.Middleware, err error) {
	configured := make([]middleware.Middleware, 0, len(childConfig.Middlewares)+4)
	if childConfig.SkillLoader != nil {
		configured = append(configured, middleware.NewSkillMiddleware(childConfig.SkillLoader))
	}
	defer func() {
		if err == nil {
			return
		}
		err = errors.Join(err, closeMiddlewareResources(context.WithoutCancel(ctx), middlewares))
	}()

	filesystemConfig := childConfig.FilesystemConfig
	if filesystemConfig != nil && childConfig.Filesystem != nil {
		configured = append(configured, middleware.NewFilesystem(&middleware.FilesystemConfig{
			Filesystem: childConfig.Filesystem,
			ReadOnly:   filesystemConfig.ReadOnly, DisableExecute: filesystemConfig.DisableExecute,
			DisableApplyPatch: filesystemConfig.DisableApplyPatch, CommandTimeout: filesystemConfig.CommandTimeout,
		}))
	}
	if childConfig.WebConfig != nil {
		webConfig := *childConfig.WebConfig
		webConfig.ToolMask = tools.CombineMasks(webConfig.ToolMask, childConfig.ToolMask)
		configured = append(configured, middleware.NewWeb(&webConfig))
	}
	configured = append(configured, childConfig.Middlewares...)
	if childConfig.EnablePatchToolCalls {
		configured = append(configured, middleware.NewPatchToolCalls())
	}

	middlewares = make([]middleware.Middleware, 0, len(configured))
	for _, source := range configured {
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
	return middlewares, nil
}

// collectToolDescriptors keeps the child configuration free of tools created
// from the parent's mutable middleware instances.
func (a *DeepAgent) collectToolDescriptors(ctx context.Context, middlewares []middleware.Middleware, childConfig Config) ([]tools.Descriptor, error) {
	descriptors := append([]tools.Descriptor(nil), childConfig.ToolDescriptors...)
	for _, mw := range middlewares {
		extra, err := mw.Tools(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range extra {
			descriptors = append(descriptors, tools.Describe(item))
		}
	}
	if childConfig.HITLConfig != nil && childConfig.HITLConfig.NeedFollowUpTool {
		found, err := hasToolNamed(ctx, descriptors, "ask_user")
		if err != nil {
			return nil, err
		}
		if !found {
			descriptors = append(descriptors, tools.Descriptor{Tool: tools.GetFollowUpTool(), ReadOnly: true})
		}
	}
	if !childConfig.DisableSubAgent {
		found, err := hasToolNamed(ctx, descriptors, "task")
		if err != nil {
			return nil, err
		}
		if !found {
			names := []string{"general-purpose"}
			for _, spec := range childConfig.SubAgents {
				if spec.Name != "general-purpose" {
					names = append(names, spec.Name)
				}
			}
			runner := NewChildRunner(childConfig)
			task := tools.NewTaskTool(runner, names...)
			if childConfig.EnableSubAgentTaskStreaming {
				task = tools.NewStreamingTaskTool(runner, names...)
			}
			descriptors = append(descriptors, tools.Descriptor{
				Tool: task, ParallelSafe: true, ReadOnly: childConfig.ReadOnlyToolsOnly,
			})
		}
	}
	return descriptors, nil
}

func hasToolNamed(ctx context.Context, descriptors []tools.Descriptor, name string) (bool, error) {
	for _, descriptor := range descriptors {
		if descriptor.Tool == nil {
			continue
		}
		info, err := descriptor.Tool.Info(ctx)
		if err != nil {
			return false, err
		}
		if info != nil && info.Name == name {
			return true, nil
		}
	}
	return false, nil
}

func (a *DeepAgent) buildToolRegistry(ctx context.Context, descriptors []tools.Descriptor) (*tools.Registry, error) {
	registry, err := tools.NewRegistry(ctx, descriptors)
	if err != nil {
		return nil, err
	}
	if a.cfg.ToolInfoRewriter != nil {
		err = registry.RewriteInfo(ctx, a.cfg.ToolInfoRewriter)
		if err != nil {
			return nil, err
		}
	}
	return registry.Filter(ctx, a.cfg.ReadOnlyToolsOnly, a.cfg.ToolMask)
}

func (a *DeepAgent) bindModelTools(ctx context.Context, registry *tools.Registry) (model.ToolCallingChatModel, error) {
	infos, err := registry.ModelTools(ctx)
	if err != nil {
		return nil, err
	}
	if len(infos) == 0 {
		return a.cfg.Model, nil
	}
	return a.cfg.Model.WithTools(infos)
}

func (a *DeepAgent) buildPolicy() tools.Policy {
	if a.cfg.HITLConfig == nil || len(a.cfg.HITLConfig.ToolPolicyGates) == 0 {
		return a.cfg.Policy
	}
	return policyWithGates(a.cfg.Policy, a.cfg.HITLConfig.ToolPolicyGates)
}

func (a *DeepAgent) canExecuteToolsEagerly(middlewares []middleware.Middleware) bool {
	if !a.cfg.EnableStreamToolCall || a.cfg.ToolNodePreHandler != nil {
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

func (a *DeepAgent) buildRuntimeState(middlewares []middleware.Middleware) *types.GraphState {
	state := types.NewGraphState(nil)
	for _, mw := range middlewares {
		handler := mw.BuildStateHandler()
		if handler != nil {
			state.RegisterStateful(mw.Name(), handler)
		}
	}
	// Custom handlers override middleware defaults. Child-shared handlers are
	// owned by the parent and must not be persisted in the child checkpoint.
	for name, handler := range a.cfg.CustomGraphState {
		if handler == nil {
			continue
		}
		if a.cfg.Depth > 0 && slices.Contains(a.cfg.SubAgentSharedCustomStateNames, name) {
			state.RegisterRuntimeOnlyStateful(name, handler)
			continue
		}
		state.RegisterStateful(name, handler)
	}
	return state
}

func closeMiddlewareResources(ctx context.Context, middlewares []middleware.Middleware) error {
	var err error
	for i := len(middlewares) - 1; i >= 0; i-- {
		if closer, ok := middlewares[i].(middleware.ResourceCloser); ok {
			err = errors.Join(err, closer.Close(ctx))
		}
	}
	return err
}
func (a *DeepAgent) closeResources(ctx context.Context) error {
	a.mu.Lock()
	if closing := a.resourcesClosing; closing != nil {
		a.mu.Unlock()
		select {
		case <-closing:
			a.mu.Lock()
			err := a.resourcesCloseErr
			a.mu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if !a.resourcesOpen {
		a.mu.Unlock()
		return nil
	}
	a.resourcesOpen = false
	closing := make(chan struct{})
	a.resourcesClosing = closing
	middlewares := a.middlewares
	a.mu.Unlock()
	err := closeMiddlewareResources(context.WithoutCancel(ctx), middlewares)
	a.mu.Lock()
	a.resourcesCloseErr = err
	a.resourcesClosing = nil
	close(closing)
	a.mu.Unlock()
	return err
}
