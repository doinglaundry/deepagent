// Package thread adapts the Core Thread contract to the managed Worker boundary.
package thread

import (
	"context"
	"eino-cli/deepagent/core/compact"
	"eino-cli/deepagent/core/engine/agentthread"
	"eino-cli/manager/api"
	"eino-cli/protocol"
	"eino-cli/worker/managed"
	"errors"
	"fmt"
)

type Engine interface {
	SubmitInput(context.Context, protocol.Input) (string, error)
	ResumeRun(context.Context, protocol.Input) (string, error)
	Cancel()
	Events() <-chan protocol.Event
	Close() error
}
type Runtime struct {
	engine Engine
	thread api.Thread
}

func New(engine Engine, thread api.Thread) *Runtime { return &Runtime{engine: engine, thread: thread} }
func (r *Runtime) PostMessage(ctx context.Context, in protocol.Input) (string, error) {
	if err := in.Validate(); err != nil {
		return "", err
	}
	if in.ThreadID != "" && in.ThreadID != r.thread.ID {
		return "", fmt.Errorf("input belongs to another thread")
	}
	if in.SessionID != "" && in.SessionID != r.thread.SessionID {
		return "", fmt.Errorf("input belongs to another session")
	}
	in.ThreadID = r.thread.ID
	in.SessionID = r.thread.SessionID
	var id string
	var err error
	switch in.Kind {
	case protocol.InputResume:
		id, err = r.engine.ResumeRun(ctx, in)
	case protocol.InputUser, protocol.InputCompact:
		id, err = r.engine.SubmitInput(ctx, in)
	default:
		return "", fmt.Errorf("control messages must be handled by managed Worker")
	}
	if errors.Is(err, agentthread.ErrDraining) || errors.Is(err, agentthread.ErrBlocked) || errors.Is(err, compact.ErrStale) {
		return "", managed.ErrBusy
	}
	return id, err
}
func (r *Runtime) Cancel()                       { r.engine.Cancel() }
func (r *Runtime) Events() <-chan protocol.Event { return r.engine.Events() }
func (r *Runtime) Close() error                  { return r.engine.Close() }

var _ managed.Runtime = (*Runtime)(nil)
