package manager

import (
	"context"
	"eino-cli/deepagent/manager/api"
	"eino-cli/deepagent/protocol"
	"fmt"
)

// deliver preserves old queued resume messages whose input schema did not carry
// a kind. The durable blocked event supplies it only on exact correlation.
func (m *engine) deliver(ctx context.Context, r *record) ([]protocol.Input, error) {
	inputs, err := m.store.deliver(ctx, r)
	if err != nil {
		return nil, err
	}
	missing := make(map[int]struct{})
	for i, in := range inputs {
		if in.Kind == protocol.InputResume && in.Resume != nil && in.Resume.Kind == "" {
			missing[i] = struct{}{}
		}
	}
	var after int64
	for len(missing) > 0 {
		events, err := m.store.events(ctx, api.EventFilter{ThreadID: r.Thread.ID, After: after, Limit: 64})
		if err != nil {
			return nil, err
		}
		if len(events) == 0 {
			break
		}
		for _, event := range events {
			if event.Sequence <= after {
				return nil, fmt.Errorf("legacy resume event cursor did not advance")
			}
			after = event.Sequence
			b := event.Block
			if event.Kind != protocol.EventBlocked || b == nil || b.Kind == "" || event.ThreadID != r.Thread.ID || event.RunID != b.RunID {
				continue
			}
			for i := range missing {
				answer := inputs[i].Resume
				if answer.RunID != b.RunID || answer.CheckpointID != b.CheckpointID || answer.InterruptID != b.InterruptID {
					continue
				}
				copy := *answer
				copy.Kind = b.Kind
				inputs[i].Resume = &copy
				delete(missing, i)
			}
		}
	}
	// Unresolvable kinds remain explicit invalid input for the Thread adapter;
	// never infer approval from Approved=false or from the presence of an answer.
	return inputs, nil
}
