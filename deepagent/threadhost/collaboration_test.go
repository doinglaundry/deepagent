package threadhost

import (
	"context"
	"encoding/json"
	"testing"

	"eino-cli/deepagent/dal/db"
	"eino-cli/deepagent/dal/model"
	"eino-cli/deepagent/manager"
	"github.com/cloudwego/eino/components/tool"
)

type collaborationBackendFake struct {
	submits  []manager.SubmitRequest
	closed   int64
	threads  manager.ListThreadsResult
	messages manager.ListMessagesResult
}

func (f *collaborationBackendFake) Submit(_ context.Context, req manager.SubmitRequest) (manager.ThreadMessageResult, error) {
	f.submits = append(f.submits, req)
	id := req.ThreadID
	if id == 0 {
		id = 42
	}
	return manager.ThreadMessageResult{
		Thread:  &model.Thread{ThreadID: id, SessionID: req.SessionID, UserID: req.UserID, Status: model.ThreadStatusOpen},
		Message: &db.Message{MessageID: 99, ThreadID: id},
	}, nil
}

func (f *collaborationBackendFake) ListThreads(context.Context, manager.ListThreadsRequest) (manager.ListThreadsResult, error) {
	return f.threads, nil
}

func (f *collaborationBackendFake) ListMessages(context.Context, manager.ListMessagesRequest) (manager.ListMessagesResult, error) {
	return f.messages, nil
}

func TestCollaborationWaitReadsCanonicalMessageState(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"parts": []map[string]string{{"type": "text", "text": "review complete"}}})
	backend := &collaborationBackendFake{
		threads: manager.ListThreadsResult{Thread: &model.Thread{ThreadID: 42, Status: model.ThreadStatusOpen}},
		messages: manager.ListMessagesResult{Runs: map[string]*model.RunRecord{"run-1": {RunID: "run-1", Status: "finished"}}, Messages: []*model.Message{
			{MessageID: 99, ThreadID: 42, Status: model.MessageStatusAccepted, TriggerRunID: "run-1"},
			{MessageID: 100, ThreadID: 42, MessageType: "assistant", TriggerRunID: "run-1", Payload: payload},
		}},
	}
	middleware := newCollaborationMiddleware(backend, &model.Thread{ThreadID: 7})
	result := runCollaborationTool(t, middleware, "wait_message", `{"target":"42","message_id":"99","timeout_ms":1}`)
	if result["state"] != "completed" || result["result"] != "review complete" {
		t.Fatalf("wait result=%v", result)
	}
}

func (f *collaborationBackendFake) Close(_ context.Context, threadID int64, _ string) (*manager.ThreadMessageResult, error) {
	f.closed = threadID
	return &manager.ThreadMessageResult{}, nil
}

func runCollaborationTool(t *testing.T, middleware *collaborationMiddleware, name, input string) map[string]any {
	t.Helper()
	items, err := middleware.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		info, _ := item.Info(context.Background())
		if info.Name != name {
			continue
		}
		output, err := item.(tool.InvokableTool).InvokableRun(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		err = json.Unmarshal([]byte(output), &result)
		if err != nil {
			t.Fatalf("decode %s output %q: %v", name, output, err)
		}
		return result
	}
	t.Fatalf("tool %q not found", name)
	return nil
}

func TestCollaborationToolsUseCanonicalManagerBoundary(t *testing.T) {
	backend := &collaborationBackendFake{}
	middleware := newCollaborationMiddleware(backend, &model.Thread{
		ThreadID: 7, UserID: 3, SessionID: "session", Profile: &model.Profile{Cwd: "/repo"},
	})

	spawn := runCollaborationTool(t, middleware, "spawn_task", `{"title":"review","content":"inspect code"}`)
	if spawn["thread_id"] != "42" || len(backend.submits) != 1 || backend.submits[0].ThreadID != 0 {
		t.Fatalf("spawn result=%v requests=%+v", spawn, backend.submits)
	}
	got := backend.submits[0].Metadata["parent_thread_id"]
	if got != "7" {
		t.Fatalf("parent_thread_id=%q", got)
	}

	sent := runCollaborationTool(t, middleware, "send_message", `{"target":"42","content":"new context"}`)
	if sent["message_id"] != "99" || len(backend.submits) != 2 || backend.submits[1].ThreadID != 42 {
		t.Fatalf("send result=%v requests=%+v", sent, backend.submits)
	}

	closed := runCollaborationTool(t, middleware, "close_task", `{"target":"42","reason":"done"}`)
	if closed["closed"] != true || backend.closed != 42 {
		t.Fatalf("close result=%v target=%d", closed, backend.closed)
	}
}
