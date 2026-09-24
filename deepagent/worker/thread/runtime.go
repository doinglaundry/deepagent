// Package thread adapts the Core Thread contract to the managed Worker boundary.
package thread

import (
	"context"
	"eino-cli/deepagent/manager/api"
	"eino-cli/deepagent/protocol"
	corethread "eino-cli/deepagent/thread"
	"eino-cli/deepagent/worker/managed"
	"sync"
)

type Runtime struct {
	source     corethread.ThreadRuntime
	events     chan protocol.Event
	done       chan struct{}
	closeDone  chan struct{}
	closeOnce  sync.Once
	stopParent func() bool
	stopSource context.CancelFunc
	mu         sync.Mutex
	closed     bool
	err        error
	thread     api.Thread
}

func (r *Runtime) PostMessage(ctx context.Context, in protocol.Input) (string, error) {
	return r.postTransport(ctx, in)
}
func (r *Runtime) Cancel() {
	_ = r.source.Interrupt(context.Background(), corethread.TransportThreadInterruptRequest{Kind: corethread.TransportThreadInterruptKindCancelInput})
}
func (r *Runtime) Events() <-chan protocol.Event { return r.events }
func (r *Runtime) Close() error                  { return r.closeTransport() }

var _ managed.Runtime = (*Runtime)(nil)
