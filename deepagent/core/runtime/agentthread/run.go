package agentthread

import (
	"context"
	"eino-cli/deepagent/core/runtime/checkpointer"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	"eino-cli/deepagent/core/graph"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

type run struct {
	id              string
	agent           *graph.DeepAgent
	cancel          context.CancelCauseFunc
	done            chan struct{}
	mu              sync.Mutex
	err             error
	consumed        []Input
	owner           *DeepAgentThread
	config          RunConfig
	accepting       bool // guarded by owner.mu
	resume          *ResumeRunOptions
	interruptOpts   *InterruptOptions // guarded by mu
	interruptTimer  *time.Timer       // guarded by mu
	modelResponseID string            // accessed by the graph's serialized event callback
}

var errExternalInterruptTimeout = fmt.Errorf("external interrupt timed out: %w", context.Canceled)

func (r *run) execute(ctx context.Context) error {
	cfg := *r.config.Agent.Clone()
	if r.config.MiddlewaresProvider != nil {
		cfg.Middlewares = append(r.config.MiddlewaresProvider(ctx, r.id), cfg.Middlewares...)
	}
	if r.config.CustomStateBuilder != nil {
		if cfg.CustomGraphState == nil {
			cfg.CustomGraphState = map[string]types.RunTimeStateful{}
		}
		for name, state := range r.config.CustomStateBuilder(ctx, r.owner.ThreadID, r.id) {
			cfg.CustomGraphState[name] = state
		}
	}
	if r.config.EnablePlan {
		cfg.Middlewares = append(cfg.Middlewares, middleware.NewPlan(nil))
	}
	cfg.ThreadID = r.owner.ThreadID
	cfg.RunID = r.id
	cfg.Conversation = r.owner.conversation
	// A fresh run may follow a canceled tool exchange. Repair only the model
	// request; the durable conversation must retain the actual execution history.
	cfg.EnablePatchToolCalls = true
	cfg.DrainInput = r.owner.drainInput
	cfg.Emit = func(ctx context.Context, e types.RuntimeEvent) error {
		if e.Kind == "run_state_restored" {
			if inputs, ok := e.Data.([]types.Input); ok {
				r.mu.Lock()
				r.consumed = append([]Input(nil), inputs...)
				r.mu.Unlock()
			}
			return nil
		}
		// RunEnd is published by the run after completion hooks and cleanup.
		if e.Kind == string(EventRunEnd) {
			return nil
		}
		if e.Kind == string(EventLLMRequesting) {
			r.modelResponseID = uuid.NewString()
		}
		payload := adaptPayload(e)
		switch p := payload.(type) {
		case LLMTokenChunk:
			p.LLMResponseID = r.modelResponseID
			payload = p
		case LLMEnd:
			p.LLMResponseID = r.modelResponseID
			payload = p
		}
		return r.emit(ctx, EventType(e.Kind), payload)
	}
	agent, err := graph.New(ctx, graph.WithConfig(&cfg))
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.agent = agent
	inputs := append([]Input(nil), r.consumed...)
	r.mu.Unlock()
	defer agent.Close(context.Background())
	var messages []*schema.Message
	for _, input := range inputs {
		messages = append(messages, input.Message)
	}
	var first *schema.Message
	if len(messages) > 0 {
		first = messages[0]
	}
	if err := r.emit(ctx, EventRunStart, RunStartPayload{Input: first}); err != nil {
		return err
	}
	var metadata []any
	for _, input := range inputs {
		metadata = append(metadata, input.Meta)
	}
	options := []graph.RunOptionFunc{graph.WithInputMetadata(metadata...)}
	if cfg.CheckpointStore != nil {
		options = append(options, graph.WithCheckpointID(r.id))
	}
	if r.resume != nil {
		options = append(options, graph.WithCheckpointID(r.resume.CheckpointID), graph.WithWriteToCheckpointID(r.resume.WriteToCheckpointID), graph.WithResume(r.resume.ResumeInterruptIDs...), graph.WithResumeData(r.resume.ResumeData))
		if r.resume.ForceNewRun {
			options = append(options, graph.WithForceNewRun())
		}
	}
	_, err = agent.Run(ctx, messages, options...)
	if err == nil && r.config.RunCompleted != nil {
		r.config.RunCompleted(ctx, r.owner.ThreadID, r.id, cfg.Model, r.owner.conversation.History(ctx))
	}
	return err
}
func (r *run) interrupt(opts InterruptOptions) {
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
	agent := r.agent
	r.mu.Unlock()
	if agent == nil {
		r.cancel(context.Canceled)
		return
	}
	// Checkpoint only after the running node has settled. Eino's forced
	// interrupt timeout can serialize local state while that node still writes
	// it, and labels unfinished side effects for replay. Our timer cancels the
	// run instead when the graceful boundary cannot be reached in time.
	agent.Interrupt()
}
func (r *run) wait(ctx context.Context) error {
	select {
	case <-r.done:
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (t *DeepAgentThread) executeRun(ctx context.Context, r *run) {
	err := r.execute(ctx)
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
	// messages remain attributed to this run even if execution failed.
	t.mu.Lock()
	r.accepting = false
	r.mu.Lock()
	pending := t.pending
	r.consumed = append(r.consumed, pending...)
	t.pending = nil
	r.mu.Unlock()
	t.mu.Unlock()
	// Execution cancellation stops model/tools, but does not revoke delivery of
	// the terminal events. Keep the run active until its event consumer accepts
	// them; Wait must not report completion ahead of this boundary.
	terminalCtx := context.WithoutCancel(ctx)
	if _, interrupted := compose.ExtractInterruptInfo(err); interrupted && len(pending) > 0 {
		saveCtx, cancel := context.WithTimeout(terminalCtx, 10*time.Second)
		saveErr := checkpointer.AppendInputs(saveCtx, r.config.Agent.CheckpointStore, r.checkpointID(), t.ThreadID, r.id, pending)
		cancel()
		if saveErr != nil {
			err = fmt.Errorf("persist pending checkpoint inputs: %w", saveErr)
		}
	}
	if _, interrupted := compose.ExtractInterruptInfo(err); !interrupted && len(pending) > 0 {
		saveCtx, cancel := context.WithTimeout(terminalCtx, 10*time.Second)
		for _, input := range pending {
			if saveErr := t.conversation.AddHistory(saveCtx, r.id, input.Message); saveErr != nil {
				err = errors.Join(err, saveErr)
				break
			}
			if emitErr := r.emit(saveCtx, EventInputConsumed, input); emitErr != nil {
				err = errors.Join(err, emitErr)
				break
			}
		}
		cancel()
	}
	end := RunEndPayload{Status: "finished"}
	if info, interrupted := compose.ExtractInterruptInfo(err); interrupted {
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
		err = r.emit(terminalCtx, EventInterrupted, payload)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			end.Status = "interrupted"
		} else {
			end.Status = "failed"
		}
		_ = r.emit(terminalCtx, EventError, ErrorPayload{Message: err.Error(), Cancelled: errors.Is(err, context.Canceled)})
	}
	if finalErr := r.emit(terminalCtx, EventRunEnd, end); err == nil {
		err = finalErr
	}
	r.cancel(err)
	t.mu.Lock()
	r.mu.Lock()
	r.err = err
	r.mu.Unlock()
	if t.current == r {
		t.current = nil
	}
	close(r.done)
	t.mu.Unlock()
}
func (r *run) emit(ctx context.Context, kind EventType, payload any) error {
	r.mu.Lock()
	messages := make([]*schema.Message, 0, len(r.consumed))
	metadata := make([]any, 0, len(r.consumed))
	for _, input := range r.consumed {
		messages = append(messages, graph.CopyMessage(input.Message))
		metadata = append(metadata, input.Meta)
	}
	r.mu.Unlock()
	id := uuid.NewString()
	if r.config.EventIDProvider != nil {
		id = r.config.EventIDProvider(ctx, r.owner.ThreadID, r.id)
	}
	event := Event{ID: id, TS: time.Now(), ThreadID: r.owner.ThreadID, RunID: r.id, Type: kind, Payload: payload, ConsumedInputs: messages, ConsumedInputsMeta: metadata, Loc: EventLocation{AgentName: r.config.Agent.Name, AgentDepth: r.config.Agent.Depth}}
	select {
	case r.owner.events <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func adaptPayload(e types.RuntimeEvent) any {
	switch value := e.Data.(type) {
	case tools.PlanUpdate:
		payload := PlanUpdatedPayload{Explanation: value.Explanation}
		for _, step := range value.Plan {
			payload.Plan = append(payload.Plan, PlanStep{Step: step.Step, Status: PlanStepStatus(step.Status)})
		}
		return payload
	case types.ToolOutputChunk:
		return ToolCallOutputChunkPayload{Name: value.Call.Name, CallID: value.Call.ID, Chunk: value.Content}
	case types.ToolCallState:
		if e.Kind == string(EventToolStart) {
			return ToolStartPayload{Name: value.Call.Name, CallID: value.Call.ID, Args: value.Call.Arguments}
		}
		if e.Kind == string(EventToolEnd) && value.Result != nil {
			return ToolEndPayload{Name: value.Call.Name, CallID: value.Call.ID, ArgumentsInJSON: value.Call.Arguments, ToolStartTime: value.StartedAt, Result: value.Result.Content}
		}
	case *schema.Message:
		if e.Kind == string(EventLLMToken) {
			return LLMTokenChunk{Text: value.Content, ReasoningText: value.ReasoningContent}
		}
		if e.Kind == string(EventLLMEnd) {
			return LLMEnd{CallbackOutput: model.CallbackOutput{Message: value}}
		}
	case []*schema.Message:
		if e.Kind == string(EventLLMRequesting) {
			return LLMRequestingPayload{Messages: value}
		}
	case types.ToolResult:
		return ToolEndPayload{CallID: value.CallID, Result: value.Content}
	case string:
		if e.Kind == string(EventToolCallOutputChunk) {
			return ToolCallOutputChunkPayload{CallID: e.CallID, Chunk: value}
		}
	}
	return e.Data
}

type RunHandle struct {
	owner *DeepAgentThread
	run   *run
}

func (h *RunHandle) RunID() string {
	if h == nil || h.run == nil {
		return ""
	}
	return h.run.id
}
func (h *RunHandle) Wait(ctx context.Context) error {
	if h == nil || h.run == nil {
		return ErrInvalidOp
	}
	return h.run.wait(ctx)
}
func (h *RunHandle) IsActive() bool {
	if h == nil || h.owner == nil || h.run == nil {
		return false
	}
	h.owner.mu.Lock()
	defer h.owner.mu.Unlock()
	return h.owner.current == h.run
}
func (h *RunHandle) ConsumedInputs() []*schema.Message {
	if h == nil || h.run == nil {
		return nil
	}
	h.run.mu.Lock()
	defer h.run.mu.Unlock()
	var result []*schema.Message
	for _, input := range h.run.consumed {
		result = append(result, graph.CopyMessage(input.Message))
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

func (r *run) checkpointID() string {
	checkpointID := r.id
	if r.resume != nil {
		checkpointID = r.resume.CheckpointID
		if r.resume.WriteToCheckpointID != "" {
			checkpointID = r.resume.WriteToCheckpointID
		}
	}
	return checkpointID
}

func (r *run) emitBlocked(ctx context.Context, info *compose.InterruptInfo) error {
	checkpointID := r.checkpointID()
	r.mu.Lock()
	request := r.interruptOpts
	r.mu.Unlock()
	if request != nil {
		payload := InterruptedPayload{Source: "external", CheckpointID: checkpointID, Metadata: maps.Clone(request.Metadata)}
		if request.Timeout != nil {
			payload.TimeoutMS = request.Timeout.Milliseconds()
		}
		if err := r.emit(ctx, EventInterrupted, payload); err != nil {
			return err
		}
		return r.emit(ctx, EventInterruptInfo, info)
	}
	if len(info.InterruptContexts) > 1 {
		items := make([]InterruptBatchItem, 0, len(info.InterruptContexts))
		for _, interrupt := range info.InterruptContexts {
			items = append(items, interruptBatchItem(interrupt))
		}
		if err := r.emit(ctx, EventInterruptBatchRequested, InterruptBatchPayload{CheckpointID: checkpointID, Items: items}); err != nil {
			return err
		}
		return r.emit(ctx, EventInterruptInfo, info)
	}
	for _, interrupt := range info.InterruptContexts {
		if interrupt == nil {
			if err := r.emit(ctx, EventInterrupted, InterruptedPayload{Source: "custom", CheckpointID: checkpointID}); err != nil {
				return err
			}
			continue
		}
		switch data := interrupt.Info.(type) {
		case *tools.ApprovalInfo:
			if err := r.emit(ctx, EventApproveRequested, ApprovalRequiredPayload{InterruptID: interrupt.ID, CheckpointID: checkpointID, ApprovalInfo: data}); err != nil {
				return err
			}
		case *tools.FollowUpInfo:
			if err := r.emit(ctx, EventFollowUpRequested, FollowUpRequestedPayload{InterruptID: interrupt.ID, CheckpointID: checkpointID, Info: data}); err != nil {
				return err
			}
		case *tools.ReviewEditInfo:
			if err := r.emit(ctx, EventApproveRequested, ApprovalRequiredPayload{InterruptID: interrupt.ID, CheckpointID: checkpointID, ReviewEditInfo: data}); err != nil {
				return err
			}
		default:
			if err := r.emit(ctx, EventInterrupted, InterruptedPayload{Source: "custom", InterruptID: interrupt.ID, CheckpointID: checkpointID, InfoType: fmt.Sprintf("%T", data), Info: data}); err != nil {
				return err
			}
		}
	}
	if len(info.InterruptContexts) == 0 {
		if err := r.emit(ctx, EventInterrupted, InterruptedPayload{Source: "external", CheckpointID: checkpointID}); err != nil {
			return err
		}
	}
	return r.emit(ctx, EventInterruptInfo, info)
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
	case *tools.ReviewEditInfo:
		item.Kind, item.ReviewEditInfo = InterruptItemReviewEdit, data
	default:
		item.Kind = InterruptItemCustom
	}
	return item
}
