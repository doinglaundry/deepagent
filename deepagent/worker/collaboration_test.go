package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/components/tool"
)

type collaborationBackendFake struct {
	submits  []agentmodel.SubmitRequest
	closed   int64
	threads  agentmodel.ListThreadsResult
	messages agentmodel.ListMessagesResult
}

func (f *collaborationBackendFake) Submit(_ context.Context, req agentmodel.SubmitRequest) (agentmodel.ThreadMessageResult, error) {
	f.submits = append(f.submits, req)
	id := req.ThreadID
	if id == 0 {
		id = 42
	}
	return agentmodel.ThreadMessageResult{
		Thread:  &agentmodel.ThreadRecord{ThreadID: id, SessionID: req.SessionID, UserID: req.UserID, Status: agentmodel.ThreadStatusOpen},
		Message: &agentmodel.MailboxMessage{MessageID: 99, ThreadID: id},
	}, nil
}

func (f *collaborationBackendFake) ListThreads(context.Context, agentmodel.ListThreadsRequest) (agentmodel.ListThreadsResult, error) {
	return f.threads, nil
}

func (f *collaborationBackendFake) ListMessages(context.Context, agentmodel.ListMessagesRequest) (agentmodel.ListMessagesResult, error) {
	return f.messages, nil
}

func TestCollaborationWaitReadsCanonicalMessageState(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"parts": []map[string]string{{"type": "text", "text": "review complete"}}})
	backend := &collaborationBackendFake{
		threads: agentmodel.ListThreadsResult{Thread: &agentmodel.ThreadRecord{ThreadID: 42, Status: agentmodel.ThreadStatusOpen}},
		messages: agentmodel.ListMessagesResult{Runs: map[string]*agentmodel.RunRecord{"run-1": {RunID: "run-1", Status: "finished"}}, Messages: []*agentmodel.MailboxMessage{
			{MessageID: 99, ThreadID: 42, Status: agentmodel.MessageStatusAccepted, TriggerRunID: "run-1"},
			{MessageID: 100, ThreadID: 42, MessageType: "assistant", TriggerRunID: "run-1", Payload: payload},
		}},
	}
	items, err := newCollaborationTools(backend, &agentmodel.ThreadRecord{ThreadID: 7})
	if err != nil {
		t.Fatal(err)
	}
	result := runCollaborationTool(t, items, "wait_message", `{"target":"42","message_id":"99","timeout_ms":1}`)
	if result["state"] != "completed" || result["result"] != "review complete" {
		t.Fatalf("wait result=%v", result)
	}
}

func (f *collaborationBackendFake) Close(_ context.Context, threadID int64, _ string) (*agentmodel.ThreadMessageResult, error) {
	f.closed = threadID
	return &agentmodel.ThreadMessageResult{}, nil
}

func runCollaborationTool(t *testing.T, items []agentmodel.ToolDescriptor, name, input string) map[string]any {
	t.Helper()
	for _, item := range items {
		info, _ := item.Tool.Info(context.Background())
		if info.Name != name {
			continue
		}
		output, err := item.Tool.(tool.InvokableTool).InvokableRun(context.Background(), input)
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
	items, err := newCollaborationTools(backend, &agentmodel.ThreadRecord{
		ThreadID: 7, UserID: 3, SessionID: "session", Profile: &agentmodel.ThreadProfile{Cwd: "/repo"},
	})

	if err != nil {
		t.Fatal(err)
	}
	spawn := runCollaborationTool(t, items, "spawn_task", `{"title":"review","content":"inspect code"}`)
	if spawn["thread_id"] != "42" || len(backend.submits) != 1 || backend.submits[0].ThreadID != 0 {
		t.Fatalf("spawn result=%v requests=%+v", spawn, backend.submits)
	}
	got := backend.submits[0].Metadata["parent_thread_id"]
	if got != "7" {
		t.Fatalf("parent_thread_id=%q", got)
	}

	sent := runCollaborationTool(t, items, "send_message", `{"target":"42","content":"new context"}`)
	if sent["message_id"] != "99" || len(backend.submits) != 2 || backend.submits[1].ThreadID != 42 {
		t.Fatalf("send result=%v requests=%+v", sent, backend.submits)
	}

	closed := runCollaborationTool(t, items, "close_task", `{"target":"42","reason":"done"}`)
	if closed["closed"] != true || backend.closed != 42 {
		t.Fatalf("close result=%v target=%d", closed, backend.closed)
	}
}

// A storage read can reach the wait deadline before the polling select does.
type deadlineCollaborationBackend struct {
	collaborationBackendFake
	readError error
}

func (backend *deadlineCollaborationBackend) ListThreads(ctx context.Context, _ agentmodel.ListThreadsRequest) (agentmodel.ListThreadsResult, error) {
	<-ctx.Done()
	if backend.readError != nil {
		return agentmodel.ListThreadsResult{}, backend.readError
	}
	return agentmodel.ListThreadsResult{}, ctx.Err()
}

func TestCollaborationWaitDeadlineDuringRead(t *testing.T) {
	backend := &deadlineCollaborationBackend{}
	tools := &collaborationTools{manager: backend}
	input := &collaborationWaitInput{Target: "42", MessageID: "99", TimeoutMS: 1}
	output, err := tools.wait(context.Background(), input)
	if err != nil || output != `{"state":"waiting"}` {
		t.Fatalf("wait deadline: output=%q err=%v", output, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = tools.wait(ctx, input)
	if err != context.Canceled {
		t.Fatalf("parent cancellation was swallowed: %v", err)
	}
}

func TestCollaborationWaitPreservesStorageFailureAtDeadline(t *testing.T) {
	readError := errors.New("storage connection failed")
	tools := &collaborationTools{manager: &deadlineCollaborationBackend{readError: readError}}
	_, err := tools.wait(context.Background(), &collaborationWaitInput{Target: "42", MessageID: "99", TimeoutMS: 1})
	if !errors.Is(err, readError) {
		t.Fatalf("storage failure was swallowed: %v", err)
	}
}
