package run

import (
	"context"
	"errors"

	"eino-cli/deepagent/graph/types"
	messagepkg "eino-cli/deepagent/message"
)

// Handle observes a Run without controlling Graph execution.
type Handle struct{ run *Run }

func (r *Run) Handle() *Handle { return &Handle{run: r} }
func (h *Handle) RunID() string {
	if h == nil || h.run == nil {
		return ""
	}
	return h.run.ID()
}
func (h *Handle) Wait(ctx context.Context) error {
	if h == nil || h.run == nil {
		return errors.New("invalid run handle")
	}
	return h.run.Wait(ctx)
}
func (h *Handle) IsActive() bool {
	return h != nil && h.run != nil && h.run.IsActive()
}
func (h *Handle) ConsumedInputs() []*messagepkg.Message {
	if h == nil || h.run == nil {
		return nil
	}
	var result []*messagepkg.Message
	for _, input := range h.run.Inputs() {
		result = append(result, types.CopyMessage(input.Message))
	}
	return result
}
func (h *Handle) ConsumedInputsMeta() []any {
	if h == nil || h.run == nil {
		return nil
	}
	var result []any
	for _, input := range h.run.Inputs() {
		result = append(result, input.Meta)
	}
	return result
}
