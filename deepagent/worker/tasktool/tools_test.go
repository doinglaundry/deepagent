package tasktool

import (
	"context"
	"eino-cli/deepagent/manager/api"
	"eino-cli/deepagent/manager/compat"
	"eino-cli/deepagent/protocol"
	"testing"
)

type testManager struct {
	api.Manager
	request   api.CreateThreadRequest
	thread    api.Thread
	submitted bool
	closed    bool
}

func (m *testManager) CreateThread(_ context.Context, r api.CreateThreadRequest) (api.Thread, error) {
	m.request = r
	return api.Thread{ID: "child", SessionID: r.SessionID, ParentID: r.ParentID}, nil
}
func (m *testManager) GetThread(context.Context, string) (api.Thread, error) { return m.thread, nil }
func (m *testManager) SubmitInput(_ context.Context, _ string, i protocol.Input) (protocol.Input, error) {
	m.submitted = true
	return i, nil
}
func (m *testManager) RequestThreadClose(context.Context, string) error { m.closed = true; return nil }
func TestChildSharesSessionAndWorkDir(t *testing.T) {
	m := &testManager{}
	s := New(m, api.Thread{ID: "parent", SessionID: "session", WorkDir: "/work", PlanMode: true})
	child, err := s.Create(context.Background(), CreateArgs{Name: "research", Prompt: "Find answer"})
	if err != nil {
		t.Fatal(err)
	}
	if child.SessionID != "session" || m.request.ParentID != "parent" || m.request.WorkDir != "/work" || !m.request.PlanMode || m.request.Input.Text != "Find answer" {
		t.Fatalf("bad child request %+v", m.request)
	}
}
func TestCrossSessionAndUnrelatedThreadRejected(t *testing.T) {
	for _, thread := range []api.Thread{{ID: "foreign", SessionID: "elsewhere", ParentID: "parent"}, {ID: "peer", SessionID: "session", ParentID: "other"}} {
		m := &testManager{thread: thread}
		s := New(m, api.Thread{ID: "parent", SessionID: "session"})
		if _, err := s.Send(context.Background(), SendArgs{ThreadID: thread.ID, Message: "change task"}); err == nil {
			t.Fatal("cross-boundary send accepted")
		}
		if err := s.Close(context.Background(), thread.ID); err == nil {
			t.Fatal("cross-boundary close accepted")
		}
		if m.submitted || m.closed {
			t.Fatal("mutated unrelated thread")
		}
	}
}

func TestReadableChildNameResolvesAcrossServiceInstances(t *testing.T) {
	m := manager.NewMemory("names")
	ctx := context.Background()
	parent, err := m.CreateThread(ctx, api.CreateThreadRequest{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s := New(m, parent)
	child, err := s.Create(ctx, CreateArgs{Name: "research", Prompt: "Investigate"})
	if err != nil {
		t.Fatal(err)
	}
	reloaded := New(m, parent)
	input, err := reloaded.Send(ctx, SendArgs{ThreadID: "research", Message: "Focus on tests"})
	if err != nil {
		t.Fatal(err)
	}
	if input.ThreadID != child.ID {
		t.Fatalf("sent to %q not %q", input.ThreadID, child.ID)
	}
}
