package thread

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sync"

	"eino-cli/deepagent/graph/tools"
	agentmodel "eino-cli/deepagent/model"
)

func (t *Thread) runOutputBridge(ctx context.Context, bridge *threadOutputBridge) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-bridge.finish:
			// The owning Core has stopped producing. Drain queued events
			// without canceling their delivery before closing the output.
			drainCtx := context.WithoutCancel(ctx)
			for {
				select {
				case ev, ok := <-bridge.agentEvents:
					if !ok {
						bridge.agentEvents = nil
						continue
					}
					if !t.forwardAgentEvent(drainCtx, ev) {
						return
					}
				case item := <-bridge.inbox:
					if !bridge.deliver(drainCtx, t, item) {
						return
					}
				default:
					return
				}
			}
		case ev, ok := <-bridge.agentEvents:
			if !ok {
				return
			}
			if !t.forwardAgentEvent(ctx, ev) {
				return
			}
		case item := <-bridge.inbox:
			if !bridge.deliver(ctx, t, item) {
				return
			}
		}
	}
}

func (t *Thread) startThreadOutputObserver(ctx context.Context) {
	if t.threadOutputObserver == nil {
		return
	}
	t.observerOnce.Do(func() {
		t.observerQueue = make(chan agentmodel.ThreadOutputObservation, threadOutputObserverQueueSize)
		observerCtx, cancel := context.WithCancel(ctx)
		t.observerCancel = cancel
		go t.runThreadOutputObserver(observerCtx)
	})
}

func (t *Thread) runThreadOutputObserver(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case obs := <-t.observerQueue:
			t.callThreadOutputObserver(ctx, obs)
		}
	}
}

func (t *Thread) enqueueThreadOutputObservation(ctx context.Context, observation agentmodel.ThreadOutputObservation) {
	select {
	case t.observerQueue <- observation:
	default:

	}
}

func (t *Thread) threadOutputObservation(item agentmodel.TransportThreadOutputItem) (agentmodel.ThreadOutputObservation, bool) {
	if t.threadOutputObserver == nil || t.observerQueue == nil {
		return agentmodel.ThreadOutputObservation{}, false
	}
	return agentmodel.ThreadOutputObservation{
		SessionID: t.sessionID,
		ThreadID:  t.ThreadID,
		Item:      cloneThreadOutputItem(item),
	}, true
}

func (t *Thread) callThreadOutputObserver(ctx context.Context, obs agentmodel.ThreadOutputObservation) {
	defer func() {
		_ = recover()
	}()
	t.threadOutputObserver(ctx, obs)
}

func (t *Thread) forwardAgentEvent(ctx context.Context, ev agentmodel.RunEvent) bool {
	usage := t.ContextManager().GetContextUsage()
	item, err := threadOutputItem(t.sessionID, t.ThreadID, ev, &usage)
	if err != nil {
		return t.outputBridge.deliver(ctx, t, agentmodel.TransportThreadOutputItem{Err: err})
	}
	if item == nil {
		return true
	}

	if ev.Type == agentmodel.EventRunEnd && t.runFinishedObserver != nil {
		t.runFinishedObserver(ctx, ev)
	}
	return t.outputBridge.deliver(ctx, t, *item)
}

func (t *Thread) emitAgentEvent(ctx context.Context, ev agentmodel.RunEvent) {
	t.mu.Lock()
	bridge := t.outputBridge
	t.mu.Unlock()
	if bridge == nil {
		return
	}
	usage := t.ContextManager().GetContextUsage()
	item, err := threadOutputItem(t.sessionID, t.ThreadID, ev, &usage)
	if err != nil {
		bridge.send(ctx, agentmodel.TransportThreadOutputItem{Err: err})
		return
	}
	if item == nil {
		return
	}

	bridge.send(ctx, *item)
}

const metadataAgentEventID = "agent_event_id"

func threadOutputItem(sessionID string, threadID string, ev agentmodel.RunEvent, usage *agentmodel.ContextTokenUsage) (item *agentmodel.TransportThreadOutputItem, err error) {
	event, err := workerEvent(sessionID, threadID, ev, usage)
	if err != nil {
		return nil, err
	}
	yield := yieldFromAgentEvent(ev)
	if event == nil && yield == nil {
		return nil, nil
	}
	return &agentmodel.TransportThreadOutputItem{Event: event, Yield: yield}, nil
}

func yieldFromAgentEvent(ev agentmodel.RunEvent) *agentmodel.TransportThreadYield {
	switch ev.Type {
	case agentmodel.EventRunEnd:
		payload, err := agentEventPayload[agentmodel.RunEndPayload](ev)
		if err != nil {
			return &agentmodel.TransportThreadYield{Reason: "interrupted", Err: err}
		}
		switch payload.Status {
		case "", "finished":
			return &agentmodel.TransportThreadYield{Reason: "finished"}
		case "failed":
			return &agentmodel.TransportThreadYield{Reason: "failed"}
		case "interrupted":
			return &agentmodel.TransportThreadYield{Reason: "interrupted"}
		case "blocked":
			return &agentmodel.TransportThreadYield{Reason: "blocked"}
		default:
			return &agentmodel.TransportThreadYield{Reason: "interrupted", Err: fmt.Errorf("unknown run end status %q", payload.Status)}
		}
	default:
		return nil
	}
}

func workerEvent(_ string, threadID string, ev agentmodel.RunEvent, usage *agentmodel.ContextTokenUsage) (output *agentmodel.TransportEvent, err error) {
	if isHiddenInternalToolEvent(ev) {
		return nil, nil
	}

	eventType, eventPayload, err := agentEventPayloadForOutput(ev, usage)
	if err != nil {
		return nil, err
	}
	if eventPayload == nil {
		return nil, nil
	}
	payload, err := json.Marshal(eventPayload)
	if err != nil {
		return nil, err
	}
	event := &agentmodel.TransportEvent{
		ID:       ev.ID,
		ThreadID: threadID,
		RunID:    ev.RunID,
		Type:     agentmodel.TransportEventType(eventType.String()),
		Payload:  payload,
		Metadata: map[string]string{metadataAgentEventID: ev.ID},
		TS:       ev.TS,
	}
	return event, nil
}

const threadOutputBridgeBufferSize = 4096

type threadOutputBridge struct {
	agentEvents <-chan agentmodel.RunEvent
	inbox       chan agentmodel.TransportThreadOutputItem
	items       chan agentmodel.TransportThreadOutputItem
	output      *agentmodel.TransportThreadOutput
	done        chan struct{}
	finish      chan struct{}
	finishOnce  sync.Once
}

func (b *threadOutputBridge) start(ctx context.Context, runtime *Thread) *agentmodel.TransportThreadOutput {
	if b.output != nil {
		return b.output
	}
	b.inbox = make(chan agentmodel.TransportThreadOutputItem, threadOutputBridgeBufferSize)
	b.items = make(chan agentmodel.TransportThreadOutputItem, threadOutputBridgeBufferSize)
	b.done = make(chan struct{})
	b.finish = make(chan struct{})
	// Core emits terminal events after cancellation; Close owns bridge lifetime.
	bridgeCtx := context.WithoutCancel(ctx)
	go func(done chan struct{}) {
		defer close(done)
		defer close(b.items)
		runtime.runOutputBridge(bridgeCtx, b)
	}(b.done)
	b.output = &agentmodel.TransportThreadOutput{Items: b.items}
	return b.output
}

func (b *threadOutputBridge) stopAndWait(ctx context.Context) error {
	if b == nil || b.done == nil {
		return nil
	}
	b.finishOnce.Do(func() { close(b.finish) })
	select {
	case <-b.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *threadOutputBridge) send(ctx context.Context, item agentmodel.TransportThreadOutputItem) bool {
	if b == nil || b.inbox == nil {
		return false
	}
	done := b.done
	if done != nil {
		select {
		case <-done:
			return false
		default:
		}
	}
	select {
	case b.inbox <- item:
		return true
	case <-done:
		return false
	case <-ctx.Done():
		return false
	}
}

func (b *threadOutputBridge) deliver(ctx context.Context, runtime *Thread, item agentmodel.TransportThreadOutputItem) bool {
	if b == nil || b.items == nil {
		return false
	}
	observation, observe := runtime.threadOutputObservation(item)

	select {
	case b.items <- item:
		if observe {
			runtime.enqueueThreadOutputObservation(ctx, observation)
		}
		return true
	case <-ctx.Done():
		return false
	}
}

const threadOutputObserverQueueSize = 256

func cloneThreadOutputItem(item agentmodel.TransportThreadOutputItem) agentmodel.TransportThreadOutputItem {
	return agentmodel.TransportThreadOutputItem{
		Err:   item.Err,
		Event: cloneWorkerEvent(item.Event),
		Yield: cloneThreadYield(item.Yield),
	}
}

func cloneWorkerEvent(event *agentmodel.TransportEvent) *agentmodel.TransportEvent {
	if event == nil {
		return nil
	}
	clone := *event
	clone.Payload = append([]byte(nil), event.Payload...)
	clone.Metadata = maps.Clone(event.Metadata)
	return &clone
}

func cloneThreadYield(yield *agentmodel.TransportThreadYield) *agentmodel.TransportThreadYield {
	if yield == nil {
		return nil
	}
	clone := *yield
	return &clone
}

func isHiddenInternalToolEvent(ev agentmodel.RunEvent) bool {
	switch ev.Type {
	case agentmodel.EventToolStart:
		payload, ok := ev.Payload.(agentmodel.ToolStartPayload)
		return ok && payload.Name == tools.ToolUpdatePlan
	case agentmodel.EventToolCallOutputChunk:
		payload, ok := ev.Payload.(agentmodel.ToolCallOutputChunkPayload)
		return ok && payload.Name == tools.ToolUpdatePlan
	case agentmodel.EventToolEnd:
		payload, ok := ev.Payload.(agentmodel.ToolEndPayload)
		return ok && payload.Name == tools.ToolUpdatePlan
	default:
		return false
	}
}
