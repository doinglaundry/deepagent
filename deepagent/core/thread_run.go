package deepagents

import (
	"context"
	"eino-cli/deepagent/core/runtime/checkpointer"
	"errors"
	"fmt"
	"maps"
	"time"

	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

var errExternalInterruptTimeout = fmt.Errorf("external interrupt timed out: %w", context.Canceled)

func (r *Run) prepareThreadRun(ctx context.Context) ([]*schema.Message, []RunOptionFunc, error) {
	cfg := *r.config.Agent.Clone()
	if r.config.MiddlewaresProvider != nil {
		cfg.Middlewares = append(r.config.MiddlewaresProvider(ctx, r.runID), cfg.Middlewares...)
	}
	if r.config.EnablePlan {
		cfg.Middlewares = append(cfg.Middlewares, middleware.NewPlan(nil))
	}
	cfg.ThreadID = r.owner.ThreadID
	cfg.RunID = r.runID
	cfg.Conversation = r.owner.conversation
	cfg.DrainInput = r.owner.drainInput
	cfg.Emit = func(ctx context.Context, e types.RuntimeEvent) error {
		if e.Kind == "run_state_restored" {
			inputs, ok := e.Data.([]types.Input)
			if ok {
				r.owner.mu.Lock()
				r.mu.Lock()
				r.consumed = append([]Input(nil), inputs...)
				for _, input := range inputs {
					if input.MessageID != "" {
						r.owner.inputRuns[input.MessageID] = r
					}
				}
				r.mu.Unlock()
				r.owner.mu.Unlock()
			}
			return nil
		}
		// RunEnd is published by the Run after completion hooks and cleanup.
		if e.Kind == string(EventRunEnd) {
			return nil
		}
		if e.Kind == string(EventLLMRequesting) {
			r.modelResponseID = uuid.NewString()
		}
		payload := e.Data
		switch p := payload.(type) {
		case LLMTokenChunk:
			p.LLMResponseID = r.modelResponseID
			payload = p
		case LLMEnd:
			p.LLMResponseID = r.modelResponseID
			payload = p
		}
		return r.publishEvent(ctx, EventType(e.Kind), payload)
	}
	err := r.initialize(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	r.mu.Lock()
	inputs := append([]Input(nil), r.consumed...)
	r.mu.Unlock()
	var messages []*schema.Message
	for _, input := range inputs {
		messages = append(messages, input.Message)
	}
	var first *schema.Message
	if len(messages) > 0 {
		first = messages[0]
	}
	{
		err := r.publishEvent(ctx, EventRunStart, RunStartPayload{Input: first})
		if err != nil {
			return nil, nil, err
		}
	}
	var metadata []any
	var ids []string
	for _, input := range inputs {
		metadata = append(metadata, input.Meta)
		ids = append(ids, input.MessageID)
	}
	options := []RunOptionFunc{WithInputMetadata(metadata...), WithInputIDs(ids...)}
	if cfg.CheckpointStore != nil {
		options = append(options, WithCheckpointID(r.runID))
	}
	if r.resume != nil {
		options = append(options, WithCheckpointID(r.resume.CheckpointID), WithWriteToCheckpointID(r.resume.WriteToCheckpointID), WithResume(r.resume.ResumeInterruptIDs...), WithResumeData(r.resume.ResumeData))
		if r.resume.ForceNewRun {
			options = append(options, WithForceNewRun())
		}
	}
	return messages, options, nil
}
func (r *Run) requestInterrupt(opts InterruptOptions) {
	r.mu.Lock()
	request := InterruptOptions{Metadata: maps.Clone(opts.Metadata)}
	if opts.Timeout != nil {
		timeout := *opts.Timeout
		request.Timeout = &timeout
	}
	r.interruptOpts = &request
	if request.Timeout != nil && r.interruptTimer == nil {
		r.interruptTimer = time.AfterFunc(*request.Timeout, func() { r.cancel(errExternalInterruptTimeout) })
	}
	started := r.started
	r.mu.Unlock()
	if !started {
		r.cancel(context.Canceled)
		return
	}
	// Checkpoint only after the running node has settled. Eino's forced
	// interrupt timeout can serialize local state while that node still writes
	// it, and labels unfinished side effects for replay. Our timer cancels the
	// Run instead when the graceful boundary cannot be reached in time.
	r.Interrupt()
}
func (r *Run) wait(ctx context.Context) error {
	select {
	case <-r.done:
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (t *Thread) executeRun(ctx context.Context, r *Run) {
	_, err := r.Execute(ctx, nil)
	// Preparation may fail before Graph's cleanup defer is installed.
	err = errors.Join(err, r.closeResources(context.WithoutCancel(ctx)))
	r.mu.Lock()
	if r.interruptTimer != nil {
		r.interruptTimer.Stop()
	}
	request := r.interruptOpts
	r.mu.Unlock()
	timedOut := request != nil && context.Cause(ctx) == errExternalInterruptTimeout && errors.Is(err, context.Canceled)
	if timedOut {
		err = nil
	}
	// Close acceptance under the same lock as SubmitInput. Accepted pending
	// messages remain attributed to this Run even if execution failed.
	t.mu.Lock()
	r.accepting = false
	r.mu.Lock()
	pending := t.pending
	before := len(r.consumed)
	r.consumed = types.AppendInputs(r.consumed, pending...)
	pending = r.consumed[before:]
	t.pending = nil
	r.mu.Unlock()
	t.mu.Unlock()
	// Execution cancellation stops model/tools, but does not revoke delivery of
	// the terminal events. Keep the Run active until its event consumer accepts
	// them; Wait must not report completion ahead of this boundary.
	terminalCtx := context.WithoutCancel(ctx)
	info, interrupted := compose.ExtractInterruptInfo(err)
	if len(pending) > 0 {
		if interrupted {
			saveCtx, cancel := context.WithTimeout(terminalCtx, 10*time.Second)
			saveErr := checkpointer.AppendInputs(saveCtx, r.config.Agent.CheckpointStore, r.checkpointID(), t.ThreadID, r.runID, pending)
			cancel()
			if saveErr != nil {
				err = fmt.Errorf("persist pending checkpoint inputs: %w", saveErr)
				interrupted = false
			}
		}
		// If checkpoint persistence fails, retain accepted inputs in history
		// before publishing the failed outcome.
		if !interrupted {
			saveCtx, cancel := context.WithTimeout(terminalCtx, 10*time.Second)
			for _, input := range pending {
				saveErr := t.conversation.AddHistory(saveCtx, r.runID, input.Message)
				if saveErr != nil {
					err = errors.Join(err, saveErr)
					break
				}
				emitErr := r.publishEvent(saveCtx, EventInputConsumed, input)
				if emitErr != nil {
					err = errors.Join(err, emitErr)
					break
				}
			}
			cancel()
		}
	}
	end := RunEndPayload{Status: "finished"}
	if interrupted {
		end.Status = "blocked"
		end.CheckpointID = r.checkpointID()
		for _, interrupt := range info.InterruptContexts {
			if interrupt != nil && interrupt.ID != "" {
				end.InterruptID = interrupt.ID
				break
			}
		}
		if request != nil {
			end.Status = "interrupted"
		}
		err = r.emitBlocked(terminalCtx, info)
	}
	if timedOut && err == nil {
		end.Status = "interrupted"
		payload := InterruptedPayload{Source: "external", Metadata: maps.Clone(request.Metadata)}
		if request.Timeout != nil {
			payload.TimeoutMS = request.Timeout.Milliseconds()
		}
		err = r.publishEvent(terminalCtx, EventInterrupted, payload)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			end.Status = "interrupted"
		} else {
			end.Status = "failed"
		}
		_ = r.publishEvent(terminalCtx, EventError, ErrorPayload{Message: err.Error(), Cancelled: errors.Is(err, context.Canceled)})
	}
	{
		finalErr := r.publishEvent(terminalCtx, EventRunEnd, end)
		if err == nil {
			err = finalErr
		}
	}
	t.mu.Lock()
	if t.current == r {
		t.current = nil
	}
	r.complete(err)
	t.mu.Unlock()
}
func (r *Run) publishEvent(ctx context.Context, kind EventType, payload any) error {
	r.mu.Lock()
	messages := make([]*schema.Message, 0, len(r.consumed))
	metadata := make([]any, 0, len(r.consumed))
	for _, input := range r.consumed {
		messages = append(messages, CopyMessage(input.Message))
		metadata = append(metadata, input.Meta)
	}
	r.mu.Unlock()
	id := uuid.NewString()
	if r.config.EventIDProvider != nil {
		id = r.config.EventIDProvider(ctx, r.owner.ThreadID, r.runID)
	}
	event := Event{ID: id, TS: time.Now(), ThreadID: r.owner.ThreadID, RunID: r.runID, Type: kind, Payload: payload, ConsumedInputs: messages, ConsumedInputsMeta: metadata, Loc: EventLocation{AgentName: r.config.Agent.Name, AgentDepth: r.config.Agent.Depth}}
	select {
	case r.owner.events <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type RunHandle struct {
	run *Run
}

func (h *RunHandle) RunID() string {
	if h == nil || h.run == nil {
		return ""
	}
	return h.run.runID
}
func (h *RunHandle) Wait(ctx context.Context) error {
	if h == nil || h.run == nil {
		return ErrInvalidOp
	}
	return h.run.wait(ctx)
}
func (h *RunHandle) IsActive() bool {
	if h == nil || h.run == nil || h.run.owner == nil {
		return false
	}
	h.run.owner.mu.Lock()
	defer h.run.owner.mu.Unlock()
	return h.run.owner.current == h.run
}
func (h *RunHandle) ConsumedInputs() []*schema.Message {
	if h == nil || h.run == nil {
		return nil
	}
	h.run.mu.Lock()
	defer h.run.mu.Unlock()
	var result []*schema.Message
	for _, input := range h.run.consumed {
		result = append(result, CopyMessage(input.Message))
	}
	return result
}
func (h *RunHandle) ConsumedInputsMeta() []any {
	if h == nil || h.run == nil {
		return nil
	}
	h.run.mu.Lock()
	defer h.run.mu.Unlock()
	var result []any
	for _, input := range h.run.consumed {
		result = append(result, input.Meta)
	}
	return result
}

func (r *Run) checkpointID() string {
	checkpointID := r.runID
	if r.resume != nil {
		checkpointID = r.resume.CheckpointID
		if r.resume.WriteToCheckpointID != "" {
			checkpointID = r.resume.WriteToCheckpointID
		}
	}
	return checkpointID
}

func (r *Run) emitBlocked(ctx context.Context, info *compose.InterruptInfo) error {
	checkpointID := r.checkpointID()
	r.mu.Lock()
	request := r.interruptOpts
	r.mu.Unlock()
	if request != nil {
		payload := InterruptedPayload{Source: "external", CheckpointID: checkpointID, Metadata: maps.Clone(request.Metadata)}
		if request.Timeout != nil {
			payload.TimeoutMS = request.Timeout.Milliseconds()
		}
		err := r.publishEvent(ctx, EventInterrupted, payload)
		if err != nil {
			return err
		}
		return r.publishEvent(ctx, EventInterruptInfo, info)
	}
	if len(info.InterruptContexts) > 1 {
		items := make([]InterruptBatchItem, 0, len(info.InterruptContexts))
		for _, interrupt := range info.InterruptContexts {
			items = append(items, interruptBatchItem(interrupt))
		}
		err := r.publishEvent(ctx, EventInterruptBatchRequested, InterruptBatchPayload{CheckpointID: checkpointID, Items: items})
		if err != nil {
			return err
		}
		return r.publishEvent(ctx, EventInterruptInfo, info)
	}
	if len(info.InterruptContexts) == 0 {
		err := r.publishEvent(ctx, EventInterrupted, InterruptedPayload{Source: "external", CheckpointID: checkpointID})
		if err != nil {
			return err
		}
	}
	for _, interrupt := range info.InterruptContexts {
		kind := EventInterrupted
		var payload any = InterruptedPayload{Source: "custom", CheckpointID: checkpointID}
		if interrupt != nil {
			switch data := interrupt.Info.(type) {
			case *tools.ApprovalInfo:
				kind = EventApproveRequested
				payload = ApprovalRequiredPayload{InterruptID: interrupt.ID, CheckpointID: checkpointID, ApprovalInfo: data}
			case *tools.FollowUpInfo:
				kind = EventFollowUpRequested
				payload = FollowUpRequestedPayload{InterruptID: interrupt.ID, CheckpointID: checkpointID, Info: data}
			default:
				payload = InterruptedPayload{Source: "custom", InterruptID: interrupt.ID, CheckpointID: checkpointID, InfoType: fmt.Sprintf("%T", data), Info: data}
			}
		}
		err := r.publishEvent(ctx, kind, payload)
		if err != nil {
			return err
		}
	}
	return r.publishEvent(ctx, EventInterruptInfo, info)
}

func interruptBatchItem(interrupt *compose.InterruptCtx) InterruptBatchItem {
	if interrupt == nil {
		return InterruptBatchItem{Kind: InterruptItemCustom, InfoType: "<nil>"}
	}
	item := InterruptBatchItem{InterruptID: interrupt.ID, InfoType: fmt.Sprintf("%T", interrupt.Info), Info: interrupt.Info}
	switch data := interrupt.Info.(type) {
	case *tools.ApprovalInfo:
		item.Kind, item.ApprovalInfo = InterruptItemApprove, data
	case *tools.FollowUpInfo:
		item.Kind, item.FollowUpInfo = InterruptItemFollowUp, data
	default:
		item.Kind = InterruptItemCustom
	}
	return item
}
