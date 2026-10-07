package manager

import (
	"context"
	"encoding/json"
	"errors"

	"eino-cli/deepagent/dal/model"
	eventpkg "eino-cli/deepagent/protocol/event"
	inputpkg "eino-cli/deepagent/protocol/input"
)

const toolApprovalKeyPrefix = "allowed_tool:"

// IsToolAlwaysAllowed reads the task's durable permission before tool execution.
func (c *Manager) IsToolAlwaysAllowed(ctx context.Context, threadID int64, toolName string) (bool, error) {
	var thread model.Thread
	err := c.db.DB(ctx, true).Select("metadata_json").Where("thread_id = ?", threadID).Take(&thread).Error
	return thread.Metadata[toolApprovalKeyPrefix+toolName] == "true", err
}

// The tool name comes from the stored interruption, never from the browser.
// This runs in the same transaction that accepts the resume answer.
func (c *Manager) rememberToolApprovals(ctx context.Context, thread *model.Thread, resume inputpkg.ResumeRunPayload) error {
	answers := resume.Answers
	if len(answers) == 0 {
		answers = []inputpkg.ResumeAnswer{{InterruptID: resume.InterruptID, Approval: resume.Approval}}
	}
	requested := map[string]bool{}
	for _, answer := range answers {
		decision := answer.Approval
		if decision == nil || !decision.AlwaysAllow {
			continue
		}
		if !decision.Approved || decision.CancelRun {
			return errors.New("always_allow requires a positive tool approval")
		}
		requested[answer.InterruptID] = true
	}
	if len(requested) == 0 {
		return nil
	}
	var messages []*model.Message
	err := c.db.DB(ctx, true).Select("payload").Where("thread_id = ? AND trigger_turn_id = ? AND message_type IN ?", thread.ThreadID, resume.RunID, []string{"approval", "interrupt"}).Find(&messages).Error
	if err != nil {
		return err
	}
	toolNames := map[string]string{}
	for _, message := range messages {
		var approval eventpkg.ApprovalRequiredEventPayload
		err = json.Unmarshal(message.Payload, &approval)
		if err != nil {
			return err
		}
		if approval.CheckpointID != resume.CheckpointID || approval.InterruptID != resume.InterruptID {
			continue
		}
		if approval.Kind == eventpkg.InputRequiredKindApproval {
			toolNames[approval.InterruptID] = approval.ToolName
		}
		if approval.Kind == eventpkg.InputRequiredKindBatch {
			var batch eventpkg.InterruptBatchRequiredEventPayload
			err = json.Unmarshal(message.Payload, &batch)
			if err != nil {
				return err
			}
			for _, item := range batch.Items {
				if item.Kind == "approve" {
					toolNames[item.InterruptID] = item.ToolName
				}
			}
		}
	}
	if thread.Metadata == nil {
		thread.Metadata = map[string]string{}
	}
	for interruptID := range requested {
		toolName := toolNames[interruptID]
		if toolName == "" {
			return errors.New("always_allow must target a stored tool approval")
		}
		thread.Metadata[toolApprovalKeyPrefix+toolName] = "true"
	}
	_, err = c.threads.Update(ctx, &model.ThreadFilter{IDs: []int64{thread.ThreadID}}, map[string]any{"metadata_json": thread.Metadata})
	return err
}
