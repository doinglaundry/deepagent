package agentthread

import (
	"context"
	"github.com/cloudwego/eino/schema"
)

// RunHandle is a read-only view of one current run owned by DeepAgentThread.
// It can be held after the thread starts another run; Wait always waits for
// this run, not for whatever run becomes current later.
type RunHandle struct {
	owner *DeepAgentThread
	run   *run
}

func (c *RunHandle) RunID() (runID string) {
	if c == nil || c.run == nil {
		return ""
	}
	return c.run.runID
}
func (c *RunHandle) Wait(ctx context.Context) (err error) {
	if c == nil || c.run == nil {
		return ErrInvalidOp
	}
	select {
	case <-c.run.done:
		return c.run.runErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *RunHandle) IsActive() (active bool) {
	if c == nil || c.owner == nil || c.run == nil {
		return false
	}
	c.owner.mu.Lock()
	defer c.owner.mu.Unlock()
	return c.owner.current == c.run
}
func (c *RunHandle) ConsumedInputs() (messages []*schema.Message) {
	if c == nil || c.owner == nil || c.run == nil {
		return nil
	}
	c.owner.mu.Lock()
	defer c.owner.mu.Unlock()
	if len(c.run.consumed) == 0 {
		return nil
	}
	messages = copyMessages(c.run.consumed)
	return messages
}
func (c *RunHandle) ConsumedInputsMeta() (metadata []any) {
	if c == nil || c.owner == nil || c.run == nil {
		return nil
	}
	c.owner.mu.Lock()
	defer c.owner.mu.Unlock()
	metadata = copyConsumedInputsMeta(c.run.consumedInputMeta)
	return metadata
}
