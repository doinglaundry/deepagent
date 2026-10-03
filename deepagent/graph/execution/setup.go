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

// configure constructs Run-local middleware, registers tools once and binds the model.
// The Thread owns the filesystem; construction failures close only middleware resources.
func (a *Graph) configure(ctx context.Context) (err error) {
	childConfig := *a.cfg.Clone()
	childConfig.RunID = a.runID
	callers, err := a.newRunMiddlewares(ctx)
	if err != nil {
		return err
	}
	middlewares := make([]middleware.Middleware, 0, len(callers)+2)
	defer func() {
		if err != nil {
			err = errors.Join(err, closeMiddlewareResources(context.WithoutCancel(ctx), callers))
		}
	}()

	descriptors := append([]tools.ToolDescriptor(nil), childConfig.ToolDescriptors...)
	if childConfig.SkillLoader != nil {
		skill := middleware.NewSkillMiddleware(childConfig.SkillLoader)
		middlewares = append(middlewares, skill)
		items, toolsErr := skill.Tools(ctx)
		if toolsErr != nil {
			return toolsErr
		}
		descriptors = appendToolDescriptors(descriptors, items)
	}
	filesystemConfig := childConfig.FilesystemConfig
	if filesystemConfig != nil && childConfig.Filesystem != nil {
		middlewares = append(middlewares, middleware.NewBasePromptMiddleware(tools.FilesystemPrompt))
		items, toolsErr := tools.NewFilesystemTools(childConfig.Filesystem, tools.FilesystemToolOptions{
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
	if childConfig.WebConfig != nil {
		items, toolsErr := tools.NewWebTools(ctx, childConfig.WebConfig)
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
	if len(childConfig.SubAgents) > 0 {
		names := make([]string, 0, len(childConfig.SubAgents))
		for _, spec := range childConfig.SubAgents {
			names = append(names, spec.Name)
		}
		task := tools.NewStreamingTaskTool(NewChildRunner(childConfig), names...)
		descriptors = append(descriptors, tools.ToolDescriptor{
			Tool: task, ParallelSafe: true, ReadOnly: childConfig.ReadOnlyToolsOnly,
		})
	}
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
	a.model = a.cfg.Model
	if len(infos) > 0 {
		a.model, err = a.cfg.Model.WithTools(infos)
		if err != nil {
			return err
		}
	}
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
