package agentthread

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"sync"
	"time"

	deepagents "eino-cli/deepagent/core"
	agentgraph "eino-cli/deepagent/core/graph"
	deeptools "eino-cli/deepagent/core/tools"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

var errExternalInterruptTimeout = errors.New("external interrupt timeout")

type run struct {
	threadID string
	runID    string
	input    *Message
	resume   *ResumeRunOptions

	agent       *deepagents.DeepAgent
	events      *runEventRecorder
	onCompleted func(context.Context)

	acceptingInput    bool
	pending           []*schema.Message
	consumed          []*schema.Message
	consumedInputMeta []any

	eventsDrained chan struct{}
	done          chan struct{}
	runErr        error

	interruptMu     sync.Mutex
	interruptOpts   InterruptOptions
	interruptActive bool
	interruptTimer  *time.Timer
	cancelRun       context.CancelCauseFunc
}

func (r *run) enqueueInput(input *Message, inputMeta any) (err error) {
	if input == nil {
		return ErrInvalidOp
	}
	if !r.acceptingInput {
		return ErrRunInputClosed
	}
	cloned := agentgraph.CopyMessage(input)
	r.pending = append(r.pending, cloned)
	r.consumed = append(r.consumed, agentgraph.CopyMessage(cloned))
	r.consumedInputMeta = append(r.consumedInputMeta, inputMeta)
	return nil
}

func (r *run) drainInput() (messages []*schema.Message) {
	if len(r.pending) == 0 {
		return nil
	}
	messages = append([]*schema.Message(nil), r.pending...)
	r.pending = nil
	return messages
}

// execute 执行一轮 Agent 图并将结束、中断或失败转换为 Run 事件。
//
// 参数:
//   - ctx: Run 生命周期上下文。
//
// 返回值:
//   - err: 普通执行错误；可识别的交互中断会转成事件。
//
// 功能特点:
//  1. 必须持续消费流才会完成执行；成功结束前等待事件回调排空。
//
// 执行流程:
//  1. 安装取消及中断超时处理。
//  2. 组装 checkpoint 和恢复选项，按需发开始事件。
//  3. 启动并消费 Agent 流，等待回调，执行完成钩子并发结束事件。
//
// 使用示例:
//   - Thread 启动新 Run 后调用；审批中断会交给 handleExecutionError。
func (r *run) execute(ctx context.Context) (err error) {
	ctx, cancel := context.WithCancelCause(ctx)
	r.interruptMu.Lock()
	r.cancelRun = cancel
	r.startInterruptTimerLocked()
	r.interruptMu.Unlock()
	defer func() {
		r.interruptMu.Lock()
		if r.interruptTimer != nil {
			r.interruptTimer.Stop()
		}
		r.cancelRun = nil
		r.interruptMu.Unlock()
		cancel(nil)
	}()
	checkpointID := fmt.Sprintf("%s:%s", r.threadID, r.runID)
	isResume := false

	defer func() {
		err = r.handleExecutionError(ctx, checkpointID, err)
	}()

	runOpts := []deepagents.RunOptionFunc{
		deepagents.WithCallbacks(r.events.callbackHandler()),
	}
	opts := r.resume
	if opts != nil {
		if opts.CheckpointID != "" {
			checkpointID = opts.CheckpointID
		}
		isResume = len(opts.ResumeData) > 0 || len(opts.ResumeInterruptIDs) > 0
		if opts.WriteToCheckpointID != "" {
			runOpts = append(runOpts, deepagents.WithWriteToCheckpointID(opts.WriteToCheckpointID))
		}
		if opts.ForceNewRun {
			runOpts = append(runOpts, deepagents.WithForceNewRun())
		}
		if len(opts.ResumeData) > 0 {
			runOpts = append(runOpts, deepagents.WithResumeData(opts.ResumeData))
		} else if len(opts.ResumeInterruptIDs) > 0 {
			runOpts = append(runOpts, deepagents.WithResume(opts.ResumeInterruptIDs...))
		}
	}
	runOpts = append(runOpts, deepagents.WithCheckpointID(checkpointID))

	if !isResume {
		r.events.emit(ctx, EventRunStart, RunStartPayload{Input: agentgraph.CopyMessage(r.input)})
	}

	var messages []*schema.Message
	if r.input != nil {
		messages = []*schema.Message{r.input}
	}
	stream, err := r.agent.Stream(ctx, messages, runOpts...)
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("[agentthread::run] stream start failed: thread_id=%s turn_id=%s checkpoint_id=%s err=%v", r.threadID, r.runID, checkpointID, err))
		return err
	}
	defer stream.Close()

	for {
		_, receiveErr := stream.Recv()
		if receiveErr == nil {
			continue
		}
		if receiveErr == io.EOF {
			break
		}
		err = receiveErr
		slog.ErrorContext(ctx, fmt.Sprintf("[agentthread::run] wait graph stream failed: thread_id=%s turn_id=%s err=%v", r.threadID, r.runID, err))
		return err
	}

	r.events.waitForCallbacks()
	if cause := context.Cause(ctx); cause == errExternalInterruptTimeout {
		return cause
	}
	if r.onCompleted != nil {
		r.onCompleted(ctx)
	}
	r.events.emit(ctx, EventRunEnd, RunEndPayload{Usage: 0.0})
	return nil
}

func (r *run) interrupt(opts InterruptOptions) (interrupted bool) {
	r.interruptMu.Lock()
	r.interruptOpts = InterruptOptions{Timeout: opts.Timeout, Metadata: maps.Clone(opts.Metadata)}
	r.interruptActive = true
	r.startInterruptTimerLocked()
	r.interruptMu.Unlock()
	if opts.Timeout == nil {
		return r.agent.Interrupt()
	}
	return r.agent.Interrupt(compose.WithGraphInterruptTimeout(*opts.Timeout / 2))
}

func (r *run) startInterruptTimerLocked() {
	if !r.interruptActive || r.interruptOpts.Timeout == nil || r.cancelRun == nil || r.interruptTimer != nil {
		return
	}
	// The graph gets half the budget to persist a checkpoint; the full timeout
	// also bounds open streams that no longer observe the graph interrupt channel.
	timeout := *r.interruptOpts.Timeout
	cancel := r.cancelRun
	r.interruptTimer = time.AfterFunc(timeout, func() {
		cancel(errExternalInterruptTimeout)
	})
}

func (r *run) handleExecutionError(ctx context.Context, checkpointID string, runErr error) (err error) {
	if runErr == nil {
		return nil
	}
	if context.Cause(ctx) == errExternalInterruptTimeout {
		r.events.waitForCallbacks()
		if payload, external := r.consumeExternalInterrupt(""); external {
			r.events.emit(ctx, EventInterrupted, payload)
			return nil
		}
	}
	info, interrupted := compose.ExtractInterruptInfo(runErr)
	if !interrupted {
		r.events.emit(ctx, EventError, ErrorPayload{Message: runErr.Error()})
		return runErr
	}

	defer r.events.emit(ctx, EventInterruptInfo, info)
	if payload, external := r.consumeExternalInterrupt(checkpointID); external {
		r.events.emit(ctx, EventInterrupted, payload)
		return nil
	}
	if len(info.InterruptContexts) == 0 {
		r.events.emit(ctx, EventInterrupted, InterruptedPayload{Source: "custom", CheckpointID: checkpointID})
		return nil
	}
	if len(info.InterruptContexts) == 1 {
		r.emitInterrupt(ctx, checkpointID, info.InterruptContexts[0])
		return nil
	}

	items := make([]InterruptBatchItem, 0, len(info.InterruptContexts))
	for _, interruptContext := range info.InterruptContexts {
		items = append(items, buildInterruptBatchItem(interruptContext))
	}
	r.events.emit(ctx, EventInterruptBatchRequested, InterruptBatchPayload{
		CheckpointID: checkpointID,
		Items:        items,
	})
	return nil
}

func (r *run) consumeExternalInterrupt(checkpointID string) (payload InterruptedPayload, found bool) {
	r.interruptMu.Lock()
	opts := r.interruptOpts
	active := r.interruptActive
	r.interruptActive = false
	r.interruptMu.Unlock()
	if !active {
		return InterruptedPayload{}, false
	}
	payload = InterruptedPayload{
		Source:       "external",
		CheckpointID: checkpointID,
		Metadata:     maps.Clone(opts.Metadata),
	}
	if opts.Timeout != nil {
		payload.TimeoutMS = opts.Timeout.Milliseconds()
	}
	return payload, true
}

func (r *run) emitInterrupt(ctx context.Context, checkpointID string, interruptContext *compose.InterruptCtx) {
	if interruptContext == nil {
		r.events.emit(ctx, EventInterrupted, InterruptedPayload{Source: "custom", CheckpointID: checkpointID})
		return
	}

	switch info := interruptContext.Info.(type) {
	case *deeptools.FollowUpInfo:
		r.events.emit(ctx, EventFollowUpRequested, FollowUpRequestedPayload{
			InterruptID:  interruptContext.ID,
			CheckpointID: checkpointID,
			Info:         info,
		})
	case *deeptools.ApprovalInfo:
		r.events.emit(ctx, EventApproveRequested, ApprovalRequiredPayload{
			InterruptID:  interruptContext.ID,
			CheckpointID: checkpointID,
			ApprovalInfo: info,
		})
	case *deeptools.ReviewEditInfo:
		r.events.emit(ctx, EventApproveRequested, ApprovalRequiredPayload{
			InterruptID:    interruptContext.ID,
			CheckpointID:   checkpointID,
			ReviewEditInfo: info,
		})
	default:
		r.events.emit(ctx, EventInterrupted, InterruptedPayload{
			Source:       "custom",
			InterruptID:  interruptContext.ID,
			CheckpointID: checkpointID,
			InfoType:     fmt.Sprintf("%T", interruptContext.Info),
			Info:         interruptContext.Info,
		})
	}
}

func buildInterruptBatchItem(interruptContext *compose.InterruptCtx) (item InterruptBatchItem) {
	if interruptContext == nil {
		item.Kind = InterruptItemCustom
		item.InfoType = "<nil>"
		return item
	}

	item.InterruptID = interruptContext.ID
	item.InfoType = fmt.Sprintf("%T", interruptContext.Info)
	item.Info = interruptContext.Info
	switch info := interruptContext.Info.(type) {
	case *deeptools.FollowUpInfo:
		item.Kind = InterruptItemFollowUp
		item.FollowUpInfo = info
	case *deeptools.ApprovalInfo:
		item.Kind = InterruptItemApprove
		item.ApprovalInfo = info
	case *deeptools.ReviewEditInfo:
		item.Kind = InterruptItemReviewEdit
		item.ReviewEditInfo = info
	default:
		item.Kind = InterruptItemCustom
	}
	return item
}
