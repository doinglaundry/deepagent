package graph

import (
	"context"
	"eino-cli/deepagent/core/runtime/checkpointer"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"encoding/json"
	"fmt"
	"github.com/cloudwego/eino/compose"
	"time"
)

func (a *DeepAgent) savePending(ctx context.Context, id string, info *compose.InterruptInfo) error {
	if a.state == nil || len(info.InterruptContexts) == 0 {
		return nil
	}
	pending := make([]types.Interrupt, 0, len(info.InterruptContexts))
	for _, interrupt := range info.InterruptContexts {
		if interrupt == nil {
			continue
		}
		data, err := json.Marshal(interrupt.Info)
		if err != nil {
			return err
		}
		item := types.Interrupt{InterruptID: interrupt.ID, CheckpointID: id, Kind: "custom", Data: data}
		switch value := interrupt.Info.(type) {
		case *tools.ApprovalInfo:
			item.Kind = "approval"
			item.CallID = value.CallID
		case *tools.FollowUpInfo:
			item.Kind = "follow_up"
		case *tools.ReviewEditInfo:
			item.Kind = "review_edit"
		}
		if item.CallID == "" {
			// A single suspended call is unambiguous. Nested/parallel contexts keep
			// their Eino identity; never guess among multiple blocked calls.
			for _, call := range a.state.Calls {
				if call.Status == types.CallBlocked {
					if item.CallID != "" {
						item.CallID = ""
						break
					}
					item.CallID = call.Call.ID
				}
			}
		}
		pending = append(pending, item)
	}
	if a.cfg.CheckpointStore != nil && id != "" {
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := checkpointer.New(a.cfg.CheckpointStore, a.cfg.ThreadID, a.state.RunID, "core-graph-v1").SavePending(saveCtx, id, pending); err != nil {
			return fmt.Errorf("persist pending interrupt metadata: %w", err)
		}
	}
	a.state.Pending = pending
	return nil
}
