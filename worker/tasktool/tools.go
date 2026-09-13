// Package tasktool exposes actual Manager-scheduled child threads as agent tools.
package tasktool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"eino-cli/manager/api"
	"eino-cli/protocol"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type Service struct {
	manager api.Manager
	parent  api.Thread
}

func New(m api.Manager, parent api.Thread) *Service { return &Service{manager: m, parent: parent} }

type CreateArgs struct {
	Name   string `json:"name"`
	Prompt string `json:"prompt"`
	Role   string `json:"role,omitempty"`
}
type SendArgs struct {
	ThreadID string `json:"thread_id"`
	Message  string `json:"message"`
}
type WaitArgs struct {
	ThreadID       string `json:"thread_id"`
	After          int64  `json:"after_sequence,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}
type WaitResult struct {
	ThreadID string          `json:"thread_id"`
	State    api.State       `json:"state"`
	Event    *protocol.Event `json:"event,omitempty"`
	Text     string          `json:"text,omitempty"`
	TimedOut bool            `json:"timed_out"`
	Cursor   int64           `json:"cursor"`
}

func (s *Service) Create(ctx context.Context, a CreateArgs) (api.Thread, error) {
	if strings.TrimSpace(a.Name) == "" || strings.TrimSpace(a.Prompt) == "" {
		return api.Thread{}, fmt.Errorf("child name and prompt required")
	}
	input := protocol.Input{Kind: protocol.InputUser, Text: a.Prompt}
	return s.manager.CreateThread(ctx, api.CreateThreadRequest{SessionID: s.parent.SessionID, ParentID: s.parent.ID, Name: a.Name, Role: a.Role, WorkDir: s.parent.WorkDir, PlanMode: s.parent.PlanMode, Input: &input})
}
func (s *Service) child(ctx context.Context, id string) (api.Thread, error) {
	t, err := s.manager.GetThread(ctx, id)
	if errors.Is(err, api.ErrNotFound) {
		var matches []api.Thread
		for offset := 0; ; offset += 200 {
			threads, e := s.manager.ListSessionThreads(ctx, s.parent.SessionID, 200, offset)
			if e != nil {
				return api.Thread{}, e
			}
			for _, candidate := range threads {
				if candidate.ParentID == s.parent.ID && candidate.Name == id {
					matches = append(matches, candidate)
				}
			}
			if len(threads) < 200 {
				break
			}
		}
		if len(matches) == 1 {
			t = matches[0]
			err = nil
		} else if len(matches) > 1 {
			return api.Thread{}, fmt.Errorf("child name is ambiguous; use its thread ID")
		}
	}
	if err != nil {
		return t, err
	}
	if t.SessionID != s.parent.SessionID || t.ParentID != s.parent.ID {
		return api.Thread{}, fmt.Errorf("thread is not a child of this task")
	}
	return t, nil
}
func (s *Service) Send(ctx context.Context, a SendArgs) (protocol.Input, error) {
	child, err := s.child(ctx, a.ThreadID)
	if err != nil {
		return protocol.Input{}, err
	}
	return s.manager.SubmitInput(ctx, child.ID, protocol.Input{Kind: protocol.InputUser, Text: a.Message})
}
func (s *Service) Close(ctx context.Context, id string) error {
	child, err := s.child(ctx, id)
	if err != nil {
		return err
	}
	return s.manager.RequestThreadClose(ctx, child.ID)
}

// Wait reconciles persisted events and state. A timeout leaves the child running.
func (s *Service) Wait(ctx context.Context, a WaitArgs) (WaitResult, error) {
	result := WaitResult{ThreadID: a.ThreadID, Cursor: a.After}
	child, err := s.child(ctx, a.ThreadID)
	if err != nil {
		return result, err
	}
	a.ThreadID = child.ID
	result.ThreadID = child.ID
	if a.TimeoutSeconds < 0 || a.TimeoutSeconds > 3600 {
		return result, fmt.Errorf("timeout_seconds must be between 0 and 3600")
	}
	if a.TimeoutSeconds == 0 {
		a.TimeoutSeconds = 60
	}
	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(a.TimeoutSeconds)*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		for {
			events, err := s.manager.ListEvents(waitCtx, api.EventFilter{ThreadID: a.ThreadID, After: result.Cursor, Limit: 200})
			if err != nil {
				if waitCtx.Err() != nil {
					break
				}
				return result, err
			}
			for _, e := range events {
				if e.Sequence > result.Cursor {
					result.Cursor = e.Sequence
				}
				if e.Kind == protocol.EventRunStarted {
					result.Event = nil
					result.Text = ""
				}
				if e.Kind == protocol.EventText {
					result.Text = e.Text
				}
				if e.Terminal() {
					copy := e
					result.Event = &copy
				}
			}
			if len(events) < 200 {
				break
			}
		}
		if waitCtx.Err() == nil {
			t, err := s.child(waitCtx, a.ThreadID)
			if err != nil {
				return result, err
			}
			result.State = t.State
			if t.State == api.Closed || t.State == api.Blocked || (t.State == api.Idle && result.Event != nil) {
				return result, nil
			}
		}
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			result.TimedOut = true
			return result, nil
		case <-ticker.C:
		}
	}
}

type taskTool struct {
	service           *Service
	name, description string
	params            map[string]*schema.ParameterInfo
}

func (t *taskTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: t.description, ParamsOneOf: schema.NewParamsOneOfByParams(t.params)}, nil
}
func (t *taskTool) ReadOnly() bool { return t.name == "wait_task" }
func (t *taskTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	var value any
	var err error
	switch t.name {
	case "create_task":
		var a CreateArgs
		if err = json.Unmarshal([]byte(args), &a); err == nil {
			value, err = t.service.Create(ctx, a)
		}
	case "send_task":
		var a SendArgs
		if err = json.Unmarshal([]byte(args), &a); err == nil {
			value, err = t.service.Send(ctx, a)
		}
	case "wait_task":
		var a WaitArgs
		if err = json.Unmarshal([]byte(args), &a); err == nil {
			value, err = t.service.Wait(ctx, a)
		}
	case "close_task":
		var a struct {
			ThreadID string `json:"thread_id"`
		}
		if err = json.Unmarshal([]byte(args), &a); err == nil {
			err = t.service.Close(ctx, a.ThreadID)
			value = map[string]any{"thread_id": a.ThreadID, "close_requested": err == nil}
		}
	default:
		err = fmt.Errorf("unknown task tool")
	}
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(value)
	return string(data), err
}
func (s *Service) Tools() []tool.BaseTool {
	str := func(desc string, required bool) *schema.ParameterInfo {
		return &schema.ParameterInfo{Type: schema.String, Desc: desc, Required: required}
	}
	return []tool.BaseTool{
		&taskTool{s, "create_task", "Create a child task in this session for another Worker. Waiting parents occupy a worker slot, so provision additional concurrency.", map[string]*schema.ParameterInfo{"name": str("Readable child task name", true), "prompt": str("Complete task instructions", true), "role": str("Configured model role", false)}},
		&taskTool{s, "send_task", "Send more instructions to one of this task's child threads.", map[string]*schema.ParameterInfo{"thread_id": str("Child thread ID or unique name", true), "message": str("Additional instructions", true)}},
		&taskTool{s, "wait_task", "Wait for a child result, block, close, or timeout. Timeout does not cancel the child.", map[string]*schema.ParameterInfo{"thread_id": str("Child thread ID or unique name", true), "after_sequence": {Type: schema.Integer, Desc: "Only consider events after this cursor"}, "timeout_seconds": {Type: schema.Integer, Desc: "Wait between 1 and 3600 seconds, default 60"}}},
		&taskTool{s, "close_task", "Request child closure. Accepted closure may still be in progress.", map[string]*schema.ParameterInfo{"thread_id": str("Child thread ID or unique name", true)}},
	}
}
