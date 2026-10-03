package run

import (
	"context"

	"eino-cli/deepagent/graph/types"

	"github.com/google/uuid"
)

// forwardGraphEvent restores input ownership and adds the model response ID
// before sending execution events to Thread's output bridge.
func (r *Run) forwardGraphEvent(ctx context.Context, event types.RuntimeEvent) error {
	if event.Kind == "run_state_restored" {
		inputs, ok := event.Data.([]types.Input)
		if ok {
			if r.config.OnRestoredInputs != nil {
				r.config.OnRestoredInputs(r, inputs)
			} else {
				r.RestoreInputs(inputs)
			}
		}
		return nil
	}
	// Thread publishes the terminal event after accepted inputs are persisted.
	if event.Kind == string(EventRunEnd) {
		return nil
	}
	if event.Kind == string(EventLLMRequesting) {
		r.modelResponseID = uuid.NewString()
	}
	payload := event.Data
	switch value := payload.(type) {
	case types.LLMTokenChunk:
		value.LLMResponseID = r.modelResponseID
		payload = value
	case types.LLMEnd:
		value.LLMResponseID = r.modelResponseID
		payload = value
	}
	return r.PublishEvent(ctx, EventType(event.Kind), payload)
}
