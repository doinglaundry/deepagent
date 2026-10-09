package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"eino-cli/deepagent/dal/model"
	"eino-cli/deepagent/manager"
	eventpkg "eino-cli/deepagent/protocol/event"
	inputpkg "eino-cli/deepagent/protocol/input"

	"eino-cli/deepagent/graph/tools"
	"github.com/cloudwego/eino/components/tool/utils"
)

type CollaborationBackend interface {
	Submit(context.Context, manager.SubmitRequest) (manager.ThreadMessageResult, error)
	ListThreads(context.Context, manager.ListThreadsRequest) (manager.ListThreadsResult, error)
	ListMessages(context.Context, manager.ListMessagesRequest) (manager.ListMessagesResult, error)
	Close(context.Context, int64, string) (*manager.ThreadMessageResult, error)
}

const collaborationPrompt = "Use spawn_task for bounded independent work, send_message only for new information, wait_message to collect a result, and close_task when a child task is no longer needed. Never target the current thread."

type collaborationTools struct {
	manager CollaborationBackend
	current *model.Thread
}

func newCollaborationTools(backend CollaborationBackend, current *model.Thread) ([]tools.ToolDescriptor, error) {
	if backend == nil || current == nil {
		return nil, nil
	}
	implementation := &collaborationTools{manager: backend, current: current}
	send, err := utils.InferTool("send_message", "Send new information to another task.", implementation.send)
	if err != nil {
		return nil, err
	}
	spawn, err := utils.InferTool("spawn_task", "Create an independent child task.", implementation.spawn)
	if err != nil {
		return nil, err
	}
	wait, err := utils.InferTool("wait_message", "Wait for a submitted message to finish or block.", implementation.wait)
	if err != nil {
		return nil, err
	}
	closeTask, err := utils.InferTool("close_task", "Close another task.", implementation.close)
	if err != nil {
		return nil, err
	}
	return []tools.ToolDescriptor{{Tool: send}, {Tool: spawn}, {Tool: wait}, {Tool: closeTask}}, nil
}

type collaborationSpawnInput struct {
	Title    string            `json:"title,omitempty"`
	Content  string            `json:"content"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

func (m *collaborationTools) spawn(ctx context.Context, input *collaborationSpawnInput) (string, error) {
	if input == nil || strings.TrimSpace(input.Content) == "" {
		return "", errors.New("content is required")
	}
	metadata := cloneStrings(input.Metadata)
	metadata["parent_thread_id"] = strconv.FormatInt(m.current.ThreadID, 10)
	result, err := m.manager.Submit(ctx, manager.SubmitRequest{
		UserID: m.current.UserID, SessionID: m.current.SessionID,
		Title: strings.TrimSpace(input.Title), Metadata: metadata, Profile: m.current.Profile,
		Input: collaborationInput(m.current.ThreadID, input.Content, nil),
	})
	if err != nil {
		return "", err
	}
	return collaborationJSON(map[string]string{
		"thread_id":  strconv.FormatInt(result.Thread.ThreadID, 10),
		"message_id": strconv.FormatInt(result.Message.MessageID, 10),
	})
}

type collaborationSendInput struct {
	Target   string            `json:"target"`
	Content  string            `json:"content"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

func (m *collaborationTools) send(ctx context.Context, input *collaborationSendInput) (string, error) {
	if input == nil || strings.TrimSpace(input.Content) == "" {
		return "", errors.New("target and content are required")
	}
	target, err := collaborationThreadID(input.Target)
	if err != nil {
		return "", err
	}
	if target == m.current.ThreadID {
		return "", errors.New("cannot send_message to the current thread")
	}
	result, err := m.manager.Submit(ctx, manager.SubmitRequest{
		ThreadID: target,
		Input:    collaborationInput(m.current.ThreadID, input.Content, input.Metadata),
	})
	if err != nil {
		return "", err
	}
	return collaborationJSON(map[string]string{"message_id": strconv.FormatInt(result.Message.MessageID, 10)})
}

type collaborationWaitInput struct {
	Target    string `json:"target"`
	MessageID string `json:"message_id"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
}

type collaborationWaitResult struct {
	State  string `json:"state"`
	Result string `json:"result,omitempty"`
}

func (m *collaborationTools) wait(ctx context.Context, input *collaborationWaitInput) (string, error) {
	if input == nil {
		return "", errors.New("target and message_id are required")
	}
	target, err := collaborationThreadID(input.Target)
	if err != nil {
		return "", err
	}
	messageID, err := collaborationThreadID(input.MessageID)
	if err != nil {
		return "", fmt.Errorf("message_id: %w", err)
	}
	timeout := time.Duration(input.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = time.Minute
	}
	if timeout > 10*time.Minute {
		timeout = 10 * time.Minute
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		result, done, err := m.observe(waitCtx, target, messageID)
		if err != nil {
			return "", err
		}
		if done {
			return collaborationJSON(result)
		}
		select {
		case <-waitCtx.Done():
			return collaborationJSON(collaborationWaitResult{State: "waiting"})
		case <-ticker.C:
		}
	}
}

func (m *collaborationTools) observe(ctx context.Context, threadID, messageID int64) (collaborationWaitResult, bool, error) {
	threads, err := m.manager.ListThreads(ctx, manager.ListThreadsRequest{ThreadID: threadID})
	if err != nil {
		return collaborationWaitResult{}, false, err
	}
	messages, err := m.manager.ListMessages(ctx, manager.ListMessagesRequest{ThreadID: threadID, Limit: 1000})
	if err != nil {
		return collaborationWaitResult{}, false, err
	}
	var input *model.Message
	for _, message := range messages.Messages {
		if message.MessageID == messageID {
			input = message
			break
		}
	}
	if input == nil {
		return collaborationWaitResult{}, false, manager.ErrMessageNotFound
	}
	if input.Status == model.MessageStatusCanceled {
		return collaborationWaitResult{State: "cancelled"}, true, nil
	}
	run := messages.Runs[input.TriggerRunID]
	if run != nil {
		switch run.Status {
		case eventpkg.RunStatusFinished:
			return collaborationWaitResult{State: "completed", Result: collaborationResponse(messages.Messages, input.TriggerRunID)}, true, nil
		case eventpkg.RunStatusInterrupted, eventpkg.RunStatusFailed:
			return collaborationWaitResult{State: "interrupted", Result: collaborationResponse(messages.Messages, input.TriggerRunID)}, true, nil
		}
	}
	if threads.Thread != nil && threads.Thread.Status == model.ThreadStatusClosed {
		return collaborationWaitResult{State: "closed", Result: collaborationResponse(messages.Messages, input.TriggerRunID)}, true, nil
	}
	if (run != nil && run.Status == eventpkg.RunStatusBlocked) || (input.TriggerRunID == "" && threads.Thread != nil && threads.Thread.LastRun != nil && threads.Thread.LastRun.Status == eventpkg.RunStatusBlocked) {
		return collaborationWaitResult{State: "blocked", Result: collaborationResponse(messages.Messages, input.TriggerRunID)}, true, nil
	}
	return collaborationWaitResult{State: "waiting"}, false, nil
}

type collaborationCloseInput struct {
	Target string `json:"target"`
	Reason string `json:"reason,omitempty"`
}

func (m *collaborationTools) close(ctx context.Context, input *collaborationCloseInput) (string, error) {
	if input == nil {
		return "", errors.New("target is required")
	}
	target, err := collaborationThreadID(input.Target)
	if err != nil {
		return "", err
	}
	if target == m.current.ThreadID {
		return "", errors.New("cannot close the current thread")
	}
	_, err = m.manager.Close(ctx, target, strings.TrimSpace(input.Reason))
	if err != nil {
		return "", err
	}
	return collaborationJSON(map[string]any{"closed": true})
}

func collaborationInput(from int64, content string, metadata map[string]string) *manager.InputMessage {
	payload, _ := json.Marshal(inputpkg.UserMessage{Parts: []inputpkg.MessagePart{{Type: inputpkg.MessagePartTypeText, Text: content}}})
	return &manager.InputMessage{
		SenderType: model.SenderTypeAgent, SenderID: strconv.FormatInt(from, 10),
		MessageType: inputpkg.MessageTypeInput, Payload: payload, Metadata: cloneStrings(metadata),
	}
}

func collaborationThreadID(value string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("expected a positive numeric id")
	}
	return id, nil
}

func collaborationResponse(messages []*model.Message, runID string) string {
	var result string
	for _, message := range messages {
		if message.TriggerRunID != runID || message.MessageType != "assistant" {
			continue
		}
		var payload eventpkg.MessageEventPayload
		if json.Unmarshal(message.Payload, &payload) != nil {
			continue
		}
		var text []string
		for _, part := range payload.Parts {
			if part.Type == eventpkg.MessagePartTypeText && strings.TrimSpace(part.Text) != "" {
				text = append(text, part.Text)
			}
		}
		if len(text) > 0 {
			result = strings.Join(text, "\n")
		}
	}
	return result
}

func cloneStrings(source map[string]string) map[string]string {
	result := make(map[string]string, len(source)+1)
	for key, value := range source {
		result[key] = value
	}
	return result
}

func collaborationJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	return string(data), err
}
