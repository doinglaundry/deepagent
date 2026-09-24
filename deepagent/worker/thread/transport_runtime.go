package thread

import (
	"context"
	"errors"
	"fmt"

	canonical "eino-cli/deepagent/core/runtime/agentthread"
	"eino-cli/deepagent/manager/api"
	"eino-cli/deepagent/protocol"
	corethread "eino-cli/deepagent/thread"
	"eino-cli/deepagent/worker/managed"
)

// NewTransport connects the existing Thread adapter to the managed Worker.
// Its output remains alive after execution cancellation until Close drains it.
func NewTransport(ctx context.Context, source corethread.ThreadRuntime, owner api.Thread) (*Runtime, error) {
	if source == nil {
		return nil, fmt.Errorf("thread runtime required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stopInit := context.AfterFunc(ctx, cancel)
	output, err := source.Init(lifetime)
	stopInit()
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && (output == nil || output.Items == nil) {
		err = fmt.Errorf("thread output required")
	}
	if err != nil {
		cancel()
		return nil, errors.Join(err, source.Close(context.Background()))
	}
	r := &Runtime{source: source, thread: owner, events: make(chan protocol.Event, 256), done: make(chan struct{}), closeDone: make(chan struct{}), stopSource: cancel}
	r.stopParent = context.AfterFunc(ctx, r.Cancel)
	go r.forwardTransport(output.Items)
	return r, nil
}
func (r *Runtime) postTransport(ctx context.Context, in protocol.Input) (string, error) {
	r.mu.Lock()
	owner := r.thread
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return "", corethread.TransportErrThreadClosed
	}
	if owner.Block != nil && in.Kind != protocol.InputResume {
		return "", managed.ErrBusy
	}
	message, err := transportInput(in, owner)
	if err != nil {
		return "", err
	}
	posted, err := r.source.PostMessage(ctx, message)
	if errors.Is(err, canonical.ErrThreadRunning) || errors.Is(err, canonical.ErrThreadBackpressure) {
		return "", managed.ErrBusy
	}
	if err != nil {
		return "", err
	}
	if posted == nil {
		return "", fmt.Errorf("thread returned no run identity")
	}
	if in.Kind == protocol.InputResume {
		r.mu.Lock()
		if r.thread.Block == owner.Block {
			r.thread.Block = nil
		}
		r.mu.Unlock()
	}
	return posted.RunID, nil
}
func (r *Runtime) forwardTransport(items <-chan corethread.TransportThreadOutputItem) {
	defer close(r.done)
	defer close(r.events)
	r.mu.Lock()
	owner := r.thread
	r.mu.Unlock()
	bridge := managedOutput{owner: owner}
	failed := false
	for item := range items {
		if failed {
			continue
		}
		events, err := bridge.convert(item)
		if err != nil {
			r.mu.Lock()
			r.err = errors.Join(r.err, err)
			r.mu.Unlock()
			failed = true
			r.Cancel()
			// Close waits for this loop. Run it independently while continuing to
			// drain the source, including terminal events already queued there.
			go r.Close()
			continue
		}
		for _, event := range events {
			if event.Kind == protocol.EventBlocked {
				r.mu.Lock()
				r.thread.Block = event.Block
				r.mu.Unlock()
			}
			r.events <- event
		}
	}
}
func (r *Runtime) closeTransport() error {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		r.mu.Unlock()
		err := r.source.Close(context.Background())
		r.stopParent()
		r.stopSource()
		<-r.done
		r.mu.Lock()
		r.err = errors.Join(r.err, err)
		r.mu.Unlock()
		close(r.closeDone)
	})
	<-r.closeDone
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}
