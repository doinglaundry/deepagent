// Package deepagents 提供基于中间件架构的深度代理实现
package deepagents

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"eino-cli/deepagent/core/backends"
	"eino-cli/deepagent/core/constant"
	"eino-cli/deepagent/core/hook"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// DeepAgent 实现了基于中间件架构的深度代理。
type DeepAgent struct {
	runnable              compose.Runnable[[]*schema.Message, *schema.Message]
	middlewareChain       *middleware.MiddlewareChain
	backend               backends.Backend
	callbacks             []callbacks.Handler
	hooks                 hook.HooksChain
	graphState            *types.GraphState
	graphInterruptMu      sync.Mutex
	graphInterruptHandle  middleware.GraphInterruptHandle
	graphInterruptUsed    bool
	pendingGraphInterrupt []compose.GraphInterruptOption
	depth                 int
}

// GetGraphRunnable 返回编译完成的 Eino Runnable。
func (a *DeepAgent) GetGraphRunnable() compose.Runnable[[]*schema.Message, *schema.Message] {
	return a.runnable
}

// GraphState 返回 Agent 的持久化图状态。
func (a *DeepAgent) GraphState() *types.GraphState {
	return a.graphState
}

// Depth 返回当前 Agent 的嵌套深度。
func (a *DeepAgent) Depth() int {
	return a.depth
}

// Name 返回 Agent 对应的图名称。
func (a *DeepAgent) Name() string {
	return constant.GraphName
}

func (a *DeepAgent) newRunContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, "deep_agent", a)
}

// prepareRun 为同步或流式执行准备中断、回调与 checkpoint 上下文。
//
// 参数:
//   - ctx: 原始执行上下文。
//   - opts: 本次执行和恢复选项。
//
// 返回值:
//   - context.Context: 附加状态后的上下文。
//   - *RunOptions: 交给图执行的选项。
//   - error: BeforeAgent 钩子失败。
//
// 功能特点:
//  1. 恢复附加图状态失败只记警告；不在此直接阻止图执行。
//
// 执行流程:
//  1. 建立中断句柄并调用 BeforeAgent。
//  2. 追加回调和 checkpoint 选项。
//  3. 设置恢复数据，绑定 GraphState 和 Agent 上下文。
//
// 使用示例:
//   - Run 和 Stream 共用此准备过程。
func (a *DeepAgent) prepareRun(ctx context.Context, opts []RunOptionFunc) (context.Context, *RunOptions, error) {
	runOpt := applyRunOptions(opts)
	runCtx, interrupt := compose.WithGraphInterrupt(ctx)
	a.setGraphInterruptHandle(interrupt)
	ctx = runCtx
	if err := a.middlewareChain.BeforeAgent(ctx); err != nil {
		return ctx, nil, fmt.Errorf("middleware BeforeAgent failed: %w", err)
	}
	if err := a.hooks.BeforeAgent(ctx); err != nil {
		return ctx, nil, fmt.Errorf("hooks BeforeAgent failed: %w", err)
	}
	if len(a.callbacks) > 0 {
		runOpt.composeOpts = append(runOpt.composeOpts, compose.WithCallbacks(a.callbacks...))
	}
	if runOpt.CheckpointID != "" {
		runOpt.composeOpts = append(runOpt.composeOpts, compose.WithCheckPointID(runOpt.CheckpointID))
	}
	if runOpt.WriteToCheckpointID != "" {
		runOpt.composeOpts = append(runOpt.composeOpts, compose.WithWriteToCheckPointID(runOpt.WriteToCheckpointID))
	}
	if runOpt.ForceNewRun {
		runOpt.composeOpts = append(runOpt.composeOpts, compose.WithForceNewRun())
	}
	resumeMode := false
	if len(runOpt.ResumeData) > 0 {
		ctx = compose.BatchResumeWithData(ctx, runOpt.ResumeData)
		resumeMode = true
	} else if len(runOpt.ResumeInterruptIDs) > 0 {
		ctx = compose.Resume(ctx, runOpt.ResumeInterruptIDs...)
		resumeMode = true
	}
	if resumeMode {
		if err := a.graphState.Resume(ctx, runOpt.CheckpointID); err != nil {
			slog.WarnContext(ctx, fmt.Sprintf("[DeepAgent::prepareRun] resume graph state failed: %v", err))
		}
	}
	runCtx = types.NewStateContext(ctx, a.graphState)
	runCtx = a.newRunContext(runCtx)
	return runCtx, runOpt, nil
}

// Run 同步执行 Agent。
func (a *DeepAgent) Run(ctx context.Context, messages []*schema.Message, opts ...RunOptionFunc) (*schema.Message, error) {
	ctx, runOpt, err := a.prepareRun(ctx, opts)
	if err != nil {
		return nil, err
	}
	result, err := a.runnable.Invoke(ctx, messages, runOpt.composeOpts...)
	if err != nil {
		if _, ok := compose.ExtractInterruptInfo(err); ok {
			_ = a.graphState.Save(ctx, runOpt.CheckpointID)
			return nil, err
		}
		slog.ErrorContext(ctx, "DeepAgent graph invocation failed", "error", err)
		return result, err
	}
	if err := a.hooks.AfterAgent(ctx); err != nil {
		slog.ErrorContext(ctx, "DeepAgent hooks AfterAgent failed", "error", err)
	}
	return result, nil
}

// Stream 启动 Agent 图并返回模型消息流。
//
// 参数:
//   - ctx: 执行上下文。
//   - messages: 图的初始消息。
//   - opts: 回调、checkpoint 和恢复选项。
//
// 返回值:
//   - *schema.StreamReader: 需要消费并关闭的消息流。
//   - error: 初始化或启动失败；消费期间还可能返回错误。
//
// 功能特点:
//  1. 识别到 Eino 中断时尝试保存附加图状态，保存失败在这里被忽略。
//
// 执行流程:
//  1. 准备执行上下文。
//  2. 调用 runnable.Stream。
//  3. 处理启动错误，并包装流读取阶段的中断错误。
//
// 使用示例:
//   - run.execute 调用后反复 Recv，直到 EOF 或错误。
func (a *DeepAgent) Stream(ctx context.Context, messages []*schema.Message, opts ...RunOptionFunc) (*schema.StreamReader[*schema.Message], error) {
	ctx, runOpt, err := a.prepareRun(ctx, opts)
	if err != nil {
		return nil, err
	}
	stream, err := a.runnable.Stream(ctx, messages, runOpt.composeOpts...)
	if err != nil {
		if _, ok := compose.ExtractInterruptInfo(err); ok {
			_ = a.graphState.Save(ctx, runOpt.CheckpointID)
			return nil, err
		}
		slog.ErrorContext(ctx, "DeepAgent graph stream failed", "error", err)
		return nil, err
	}
	stream = schema.StreamReaderWithConvert(stream, func(msg *schema.Message) (*schema.Message, error) { return msg, nil }, schema.WithErrWrapper(func(err error) error {
		if _, ok := compose.ExtractInterruptInfo(err); ok {
			_ = a.graphState.Save(ctx, runOpt.CheckpointID)
		}
		return err
	}))
	return stream, nil
}

func (a *DeepAgent) setGraphInterruptHandle(handle middleware.GraphInterruptHandle) {
	if a == nil {
		return
	}
	var pending []compose.GraphInterruptOption
	a.graphInterruptMu.Lock()
	a.graphInterruptHandle = handle
	a.graphInterruptUsed = false
	if handle != nil && a.pendingGraphInterrupt != nil {
		pending = a.pendingGraphInterrupt
		a.pendingGraphInterrupt = nil
		a.graphInterruptUsed = true
	}
	a.graphInterruptMu.Unlock()
	if handle != nil && pending != nil {
		handle(pending...)
	}
}

// Interrupt 中断当前正在执行的 Agent 图；若图尚未启动，则暂存中断请求。
func (a *DeepAgent) Interrupt(opts ...compose.GraphInterruptOption) (accepted bool) {
	if a == nil {
		return false
	}
	// nil 表示没有请求；非 nil 的空切片表示无参中断。
	copiedOpts := append(make([]compose.GraphInterruptOption, 0, len(opts)), opts...)
	var handle middleware.GraphInterruptHandle
	a.graphInterruptMu.Lock()
	if a.graphInterruptHandle == nil {
		a.pendingGraphInterrupt = copiedOpts
		a.graphInterruptMu.Unlock()
		return true
	}
	if a.graphInterruptUsed {
		a.graphInterruptMu.Unlock()
		return true
	}
	handle = a.graphInterruptHandle
	a.graphInterruptUsed = true
	a.graphInterruptMu.Unlock()
	handle(copiedOpts...)
	return true
}

// Close 目前无需清理，预留接口供 subAgentRunnerAdapter 实现 SubAgentRunner。
func (a *DeepAgent) Close(ctx context.Context) error { return nil }
