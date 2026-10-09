package run

import (
	"context"

	agentmodel "eino-cli/deepagent/model"

	"github.com/google/uuid"
)

// forwardGraphEvent restores input ownership and adds the model response ID
// before sending execution events to Thread's output bridge.
func (r *Run) forwardGraphEvent(ctx context.Context, event agentmodel.RuntimeEvent) error {
	if event.Kind == "run_state_restored" {
		inputs, ok := event.Data.([]agentmodel.RunInput)
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
	if event.Kind == string(agentmodel.EventRunEnd) {
		return nil
	}
	if event.Kind == string(agentmodel.EventLLMRequesting) {
		r.modelResponseID = uuid.NewString()
	}
	payload := event.Data
	switch value := payload.(type) {
	case agentmodel.LLMTokenChunk:
		value.LLMResponseID = r.modelResponseID
		payload = value
	case agentmodel.LLMEnd:
		value.LLMResponseID = r.modelResponseID
		payload = value
	}
	return r.PublishEvent(ctx, agentmodel.RunEventType(event.Kind), payload)
}
