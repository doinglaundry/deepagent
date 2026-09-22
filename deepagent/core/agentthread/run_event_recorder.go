package agentthread

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"eino-cli/deepagent/core/constant"
	"eino-cli/deepagent/core/middlewares"
	"eino-cli/deepagent/core/middlewares/plan"
	deeptools "eino-cli/deepagent/core/tools"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	cbutils "github.com/cloudwego/eino/utils/callbacks"
	"github.com/google/uuid"
)

const eventEnqueueWarnThreshold = 50 * time.Millisecond

// runEventRecorder owns the callback-to-event protocol for one run.
// It keeps event ordering, asynchronous callback completion, and tool-event
// deduplication out of the runner execution lifecycle.
type runEventRecorder struct {
	threadID              string
	runID                 string
	contextManager        ContextManager
	eventIDProvider       func(ctx context.Context, threadID, runID string) string
	eventBus              chan Event
	eventMu               sync.RWMutex
	eventClosed           bool
	asyncCallbacks        sync.WaitGroup
	toolEvents            *toolEventMiddleware
	enableStreamToolCalls bool
	llmResponseID         string
	modelBarrierMu        sync.Mutex
	modelBarrier          *modelEventBarrier
	usageMu               sync.Mutex
	usage                 model.TokenUsage
}

func newRunEventRecorder(
	cfg *RunConfig,
	threadID string,
	runID string,
	contextManager ContextManager,
	eventBus chan Event,
) (recorder *runEventRecorder) {
	eventIDProvider := cfg.EventIDProvider
	if eventIDProvider == nil {
		eventIDProvider = func(context.Context, string, string) string {
			return uuid.NewString()
		}
	}
	return &runEventRecorder{
		threadID:              threadID,
		runID:                 runID,
		contextManager:        contextManager,
		eventIDProvider:       eventIDProvider,
		eventBus:              eventBus,
		toolEvents:            newToolEventMiddleware(),
		enableStreamToolCalls: cfg.Agent.EnableStreamToolCall,
	}
}

func (r *runEventRecorder) middlewares(enablePlan bool, _ deeptools.Mask) (middlewares []middleware.Middleware) {
	if enablePlan {
		middlewares = append(middlewares, plan.New(&plan.PlanMiddlewareConfig{
			OnPlanUpdate: func(ctx context.Context, update plan.PlanUpdate) error {
				var steps []PlanStep
				if update.Plan != nil {
					steps = make([]PlanStep, len(update.Plan))
					for i, step := range update.Plan {
						steps[i] = PlanStep{Step: step.Step, Status: PlanStepStatus(step.Status)}
					}
				}
				r.emit(ctx, EventPlanUpdated, PlanUpdatedPayload{
					Explanation: update.Explanation,
					Plan:        steps,
				})
				return nil
			},
		}))
	}
	middlewares = append(middlewares, r.toolEvents)
	return middlewares
}

func (r *runEventRecorder) callbackHandler() (handler callbacks.Handler) {
	return cbutils.NewHandlerHelper().
		Tool(&cbutils.ToolCallbackHandler{
			OnStart: func(ctx context.Context, runInfo *callbacks.RunInfo, input *tool.CallbackInput) context.Context {
				if !r.waitModelEventBarrier(ctx) {
					return ctx
				}
				callID := toolCallbackCallID(ctx, input, nil)
				state := r.toolState(callID, runInfo.Name)
				args := callbackInputArgs(input)
				if !state.markStartEmitted(time.Now(), args) {
					return ctx
				}
				r.emit(ctx, EventToolStart, ToolStartPayload{Name: runInfo.Name, CallID: callID, Args: args})
				return ctx
			},
			OnEnd: func(ctx context.Context, runInfo *callbacks.RunInfo, output *tool.CallbackOutput) context.Context {
				callID := toolCallbackCallID(ctx, nil, output)
				state := r.lookupOrCreateToolState(callID, runInfo.Name)
				if state.markStreamSeen() {
					return ctx
				}
				r.emitToolEnd(ctx, state, callbackOutputText(output))
				return ctx
			},
			OnEndWithStreamOutput: func(ctx context.Context, runInfo *callbacks.RunInfo, output *schema.StreamReader[*tool.CallbackOutput]) context.Context {
				if output == nil {
					return ctx
				}
				callID := toolCallbackCallID(ctx, nil, nil)
				state := r.lookupOrCreateToolState(callID, runInfo.Name)
				state.markStreamStarted()
				r.asyncCallbacks.Add(1)
				go func() {
					defer r.asyncCallbacks.Done()
					defer output.Close()
					for {
						chunk, err := output.Recv()
						if err != nil {
							if err == io.EOF {
								r.emitToolEnd(ctx, state, state.snapshotResult())
							} else {
								slog.ErrorContext(ctx, fmt.Sprintf("[agentthread::toolStream] recv failed: tool=%s call_id=%s err=%v", state.name, state.callID, err))
							}
							return
						}
						if chunkCallID := toolCallbackExtraCallID(chunk.Extra); chunkCallID != "" {
							state.setCallID(chunkCallID)
						}
						text := callbackOutputText(chunk)
						state.appendResult(text)
						r.emit(ctx, EventToolCallOutputChunk, ToolCallOutputChunkPayload{
							Name: state.name, CallID: state.callID, Chunk: text,
						})
					}
				}()
				return ctx
			},
		}).
		ChatModel(&cbutils.ModelCallbackHandler{
			OnStart: func(ctx context.Context, runInfo *callbacks.RunInfo, input *model.CallbackInput) context.Context {
				runName := callbackRunName(runInfo)
				r.llmResponseID = uuid.NewString()
				r.beginModelEventBarrier(ctx)
				slog.InfoContext(ctx, fmt.Sprintf("[agentthread::model] request start: thread_id=%s turn_id=%s run_name=%s input_present=%t", r.threadID, r.runID, runName, input != nil))
				if input == nil {
					r.emit(ctx, EventLLMRequesting, (*model.CallbackInput)(nil))
					return ctx
				}
				r.emit(ctx, EventLLMRequesting, input)
				return ctx
			},
			OnEnd: func(ctx context.Context, runInfo *callbacks.RunInfo, output *model.CallbackOutput) context.Context {
				barrier := r.currentModelEventBarrier()
				defer r.releaseModelEventBarrier(barrier)
				contentLength, toolCalls := modelOutputSize(output)
				slog.InfoContext(ctx, fmt.Sprintf("[agentthread::model] response end: thread_id=%s turn_id=%s run_name=%s content_len=%d tool_calls=%d usage_present=%t", r.threadID, r.runID, callbackRunName(runInfo), contentLength, toolCalls, output != nil && output.TokenUsage != nil))
				responseID := r.currentLLMResponseID()
				if output != nil {
					r.recordModelUsage(ctx, output.TokenUsage)
				}
				end := llmEndFromCallbackOutput(output, responseID)
				end.TokenUsage = r.accumulateModelUsage(end.TokenUsage)
				r.emit(ctx, EventLLMEnd, end)
				return ctx
			},
			OnEndWithStreamOutput: func(ctx context.Context, runInfo *callbacks.RunInfo, output *schema.StreamReader[*model.CallbackOutput]) context.Context {
				barrier := r.currentModelEventBarrier()
				if output == nil {
					r.releaseModelEventBarrier(barrier)
					return ctx
				}
				runName := callbackRunName(runInfo)
				responseID := r.currentLLMResponseID()
				r.asyncCallbacks.Add(1)
				go r.consumeModelStream(ctx, output, barrier, runName, responseID)
				return ctx
			},
		}).Handler()
}

func (r *runEventRecorder) consumeModelStream(
	ctx context.Context,
	output *schema.StreamReader[*model.CallbackOutput],
	barrier *modelEventBarrier,
	runName string,
	responseID string,
) {
	defer r.asyncCallbacks.Done()
	defer r.releaseModelEventBarrier(barrier)
	slog.InfoContext(ctx, fmt.Sprintf("[agentthread::modelStream] stream merge begin: thread_id=%s turn_id=%s run_name=%s", r.threadID, r.runID, runName))
	llmEnd, chunks, err := mergeModelCallbackStream(ctx, output, responseID, func(ctx context.Context, chunk *schema.Message) {
		if chunk == nil || (chunk.Content == "" && chunk.ReasoningContent == "") {
			return
		}
		r.emit(ctx, EventLLMToken, LLMTokenChunk{
			Text: chunk.Content, ReasoningText: chunk.ReasoningContent, LLMResponseID: responseID,
		})
	})
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("[agentthread::modelStream] recv failed: err=%v", err))
		return
	}
	contentLength, toolCalls := modelOutputSize(&llmEnd.CallbackOutput)
	slog.InfoContext(ctx, fmt.Sprintf("[agentthread::modelStream] stream merge done: thread_id=%s turn_id=%s run_name=%s chunks=%d content_len=%d tool_calls=%d usage_present=%t", r.threadID, r.runID, runName, chunks, contentLength, toolCalls, llmEnd.TokenUsage != nil))
	r.recordModelUsage(ctx, llmEnd.TokenUsage)
	llmEnd.TokenUsage = r.accumulateModelUsage(llmEnd.TokenUsage)
	r.emit(ctx, EventLLMEnd, llmEnd)
}

func callbackRunName(runInfo *callbacks.RunInfo) (name string) {
	if runInfo != nil {
		return runInfo.Name
	}
	return ""
}

func modelOutputSize(output *model.CallbackOutput) (contentLength int, toolCalls int) {
	if output == nil || output.Message == nil {
		return 0, 0
	}
	return len(output.Message.Content), len(output.Message.ToolCalls)
}

func (r *runEventRecorder) currentLLMResponseID() (responseID string) {
	if r.llmResponseID != "" {
		return r.llmResponseID
	}
	return uuid.NewString()
}

func (r *runEventRecorder) recordModelUsage(ctx context.Context, usage *model.TokenUsage) {
	if usage == nil || r.contextManager == nil {
		return
	}
	r.contextManager.RecordModelUsage(ctx, usage)
}

// accumulateModelUsage keeps billing usage for one logical run separate from
// ContextManager's latest context-window snapshot.
func (r *runEventRecorder) accumulateModelUsage(usage *model.TokenUsage) *model.TokenUsage {
	if usage == nil {
		return nil
	}
	r.usageMu.Lock()
	defer r.usageMu.Unlock()
	r.usage.PromptTokens += usage.PromptTokens
	r.usage.CompletionTokens += usage.CompletionTokens
	r.usage.TotalTokens += usage.TotalTokens
	r.usage.PromptTokenDetails.CachedTokens += usage.PromptTokenDetails.CachedTokens
	r.usage.CompletionTokensDetails.ReasoningTokens += usage.CompletionTokensDetails.ReasoningTokens
	return cloneModelTokenUsage(&r.usage)
}

func (r *runEventRecorder) waitForCallbacks() {
	r.asyncCallbacks.Wait()
}

func (r *runEventRecorder) emit(ctx context.Context, typ EventType, payload any) {
	location := eventLocationFromContext(ctx)
	if location == (EventLocation{}) {
		location = EventLocation{AgentName: constant.GraphName}
	}
	event := Event{
		Loc: location, ID: r.eventIDProvider(ctx, r.threadID, r.runID), TS: time.Now(),
		ThreadID: r.threadID, RunID: r.runID, Type: typ, Payload: payload,
	}
	r.eventMu.RLock()
	defer r.eventMu.RUnlock()
	if r.eventClosed {
		slog.WarnContext(ctx, fmt.Sprintf("[agentthread::emitEvent] drop late event after turn event channel closed: thread_id=%s turn_id=%s event_type=%s", r.threadID, r.runID, typ))
		return
	}
	queueLength, queueCapacity, startedAt := len(r.eventBus), cap(r.eventBus), time.Now()
	r.eventBus <- event
	if elapsed := time.Since(startedAt); elapsed > eventEnqueueWarnThreshold {
		slog.WarnContext(ctx, fmt.Sprintf("[agentthread::emitEvent] slow event enqueue: thread_id=%s turn_id=%s event_type=%s elapsed=%s queue_len_before=%d queue_cap=%d", r.threadID, r.runID, typ, elapsed, queueLength, queueCapacity))
	}
}

func (r *runEventRecorder) close() {
	r.eventMu.Lock()
	defer r.eventMu.Unlock()
	if r.eventClosed {
		return
	}
	r.eventClosed = true
	close(r.eventBus)
}

func (r *runEventRecorder) beginModelEventBarrier(ctx context.Context) (barrier *modelEventBarrier) {
	if r.enableStreamToolCalls {
		return nil
	}
	barrier = newModelEventBarrier()
	r.modelBarrierMu.Lock()
	if r.modelBarrier != nil {
		r.modelBarrier.release()
		slog.WarnContext(ctx, fmt.Sprintf("[agentthread::modelBarrier] release stale model barrier before new model call: thread_id=%s turn_id=%s", r.threadID, r.runID))
	}
	r.modelBarrier = barrier
	r.modelBarrierMu.Unlock()
	return barrier
}

func (r *runEventRecorder) currentModelEventBarrier() (barrier *modelEventBarrier) {
	if r.enableStreamToolCalls {
		return nil
	}
	r.modelBarrierMu.Lock()
	defer r.modelBarrierMu.Unlock()
	return r.modelBarrier
}

func (r *runEventRecorder) releaseModelEventBarrier(barrier *modelEventBarrier) {
	if barrier == nil {
		return
	}
	barrier.release()
	r.modelBarrierMu.Lock()
	if r.modelBarrier == barrier {
		r.modelBarrier = nil
	}
	r.modelBarrierMu.Unlock()
}

func (r *runEventRecorder) waitModelEventBarrier(ctx context.Context) (completed bool) {
	barrier := r.currentModelEventBarrier()
	if barrier == nil {
		return true
	}
	startedAt := time.Now()
	select {
	case <-barrier.done:
	case <-ctx.Done():
		slog.WarnContext(ctx, fmt.Sprintf("[agentthread::modelBarrier] stop waiting for llm_end before tool start: thread_id=%s turn_id=%s err=%v", r.threadID, r.runID, ctx.Err()))
		return false
	}
	if elapsed := time.Since(startedAt); elapsed > modelBarrierWaitWarnThreshold {
		slog.WarnContext(ctx, fmt.Sprintf("[agentthread::modelBarrier] waited for llm_end before tool start: thread_id=%s turn_id=%s elapsed=%s", r.threadID, r.runID, elapsed))
	}
	return true
}

func (r *runEventRecorder) toolState(callID string, name string) (state *toolEventState) {
	key := toolEventKey(callID, name)
	state, found := r.toolEvents.store.load(key)
	if found {
		if state.name == "" {
			state.name = name
		}
		if state.callID == "" {
			state.callID = callID
		}
		return state
	}
	return r.toolEvents.store.loadOrStore(key, &toolEventState{name: name, callID: callID})
}

func (r *runEventRecorder) lookupOrCreateToolState(callID string, name string) (state *toolEventState) {
	if callID != "" {
		return r.toolState(callID, name)
	}
	state = r.findPendingToolStateByName(name)
	if state != nil {
		return state
	}
	return r.toolState(callID, name)
}

func (r *runEventRecorder) findPendingToolStateByName(name string) (pending *toolEventState) {
	r.toolEvents.store.rangeStates(func(_ string, state *toolEventState) bool {
		if state.name == name && state.isPendingStreamBinding() {
			pending = state
			return false
		}
		return true
	})
	return pending
}

func (r *runEventRecorder) emitToolEnd(ctx context.Context, state *toolEventState, result string) {
	if state == nil || !state.markEndEmitted() {
		return
	}
	snapshot := state.snapshot()
	r.emit(ctx, EventToolEnd, ToolEndPayload{
		Name: snapshot.name, CallID: snapshot.callID, ToolStartTime: snapshot.toolStartTime,
		ArgumentsInJSON: snapshot.argumentsInJSON, Result: result,
	})
}
