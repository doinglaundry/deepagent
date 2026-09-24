package distributed

import (
	"context"
	"eino-cli/deepagent/core/graph"
	"eino-cli/deepagent/core/modelhub"
	"eino-cli/deepagent/core/runtime/agentthread"
	"eino-cli/deepagent/manager/api"
	"eino-cli/deepagent/manager/compat"
	"eino-cli/deepagent/protocol"
	corethread "eino-cli/deepagent/thread"
	"eino-cli/deepagent/worker/managed"
	workerthread "eino-cli/deepagent/worker/thread"
	"encoding/json"
	"fmt"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

func TestInaccessibleWorkDirProducesTerminalFailureOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := manager.NewMemory("bad-workdir")
	c := Config{Manager: manager.Config{Namespace: "bad-workdir", MySQLDSN: "unused", RedisAddr: "unused"}, DefaultModel: "test", Models: []modelhub.Config{{Name: "test", Provider: "openai", Model: "test", BaseURL: "http://127.0.0.1:1/v1", APIKey: "test"}}}
	factory, cleanup, err := NewFactory(ctx, m, c)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	input := protocol.Input{Kind: protocol.InputUser, Text: "hello"}
	thread, err := m.CreateThread(ctx, api.CreateThreadRequest{WorkDir: filepath.Join(t.TempDir(), "missing"), Input: &input})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := managed.New(m, factory, managed.Config{PollInterval: time.Millisecond, PermitTTL: time.Second, ShutdownGrace: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		events, err := m.ListEvents(ctx, api.EventFilter{ThreadID: thread.ID})
		if err != nil {
			t.Fatal(err)
		}
		state, err := m.GetThread(ctx, thread.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) == 1 && events[0].Kind == protocol.EventRunFailed && state.State == api.Idle {
			if len(events[0].MessageIDs) != 1 || events[0].RunID == "" || events[0].Error == "" {
				t.Fatalf("failure missing request correlation: %+v", events[0])
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("bad workdir did not surface a terminal failure")
}

type rejectingModel struct{}

func (rejectingModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("summary model unavailable")
}
func (rejectingModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, fmt.Errorf("model unavailable")
}
func (m rejectingModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func TestCoreDeliveryErrorsReachRequester(t *testing.T) {
	for _, kind := range []protocol.InputKind{protocol.InputResume, protocol.InputCompact} {
		t.Run(string(kind), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m := manager.NewMemory("delivery-error")
			initial := protocol.Input{Kind: protocol.InputUser, Text: "first"}
			thread, err := m.CreateThread(ctx, api.CreateThreadRequest{WorkDir: t.TempDir(), Input: &initial})
			if err != nil {
				t.Fatal(err)
			}
			claim, err := m.ClaimThread(ctx, thread.ID, "setup", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if err = m.ConfirmInputDelivery(ctx, claim.Permit, claim.Inputs[0].ID); err != nil {
				t.Fatal(err)
			}
			var messages []*schema.Message
			for i := 0; i < 20; i++ {
				messages = append(messages, schema.UserMessage(fmt.Sprint("history", i)))
			}
			data, _ := json.Marshal(messages)
			if _, err = m.SaveHistory(ctx, claim.Permit, api.History{Messages: data}); err != nil {
				t.Fatal(err)
			}
			initialEvent := protocol.Event{Kind: protocol.EventRunCompleted, RunID: "old", MessageIDs: []string{claim.Inputs[0].ID}}
			var block *protocol.Block
			if kind == protocol.InputResume {
				block = &protocol.Block{Kind: "approval", RunID: "old", CheckpointID: "missing", InterruptID: "gate"}
				initialEvent.Kind = protocol.EventBlocked
				initialEvent.Block = block
			}
			published, err := m.PublishEvent(ctx, claim.Permit, initialEvent)
			if err != nil {
				t.Fatal(err)
			}
			if err = m.ReleaseThread(ctx, claim.Permit, api.Release{Block: block}); err != nil {
				t.Fatal(err)
			}
			var input protocol.Input
			if kind == protocol.InputResume {
				input, err = m.ResumeFromBlock(ctx, thread.ID, protocol.Input{Kind: kind, Resume: &protocol.Resume{RunID: "old", CheckpointID: "missing", InterruptID: "gate", Approved: true}})
			} else {
				input, err = m.SubmitInput(ctx, thread.ID, protocol.Input{Kind: kind})
			}
			if err != nil {
				t.Fatal(err)
			}
			factory := func(ctx context.Context, claim api.Claim) (managed.Runtime, error) {
				bus := make(chan agentthread.Event, 64)
				core := agentthread.New(thread.ID, &agentthread.RunConfig{Agent: graph.Config{Model: rejectingModel{}, DisableSubAgent: true, CheckpointStore: Checkpoints{Manager: m, Permit: claim.Permit}}}, bus, agentthread.ThreadOptions{HistoryStore: NewHistory(ctx, m, claim.Permit), CompactionStrategy: &agentthread.SummaryCompaction{Model: rejectingModel{}, TokenLimit: 1, KeepRecent: 2}})
				adapter, err := corethread.NewThread(corethread.AdapterConfig{ThreadID: thread.ID, SessionID: thread.SessionID, Thread: core, EventBus: bus})
				if err != nil {
					return nil, err
				}
				return workerthread.NewTransport(ctx, adapter, claim.Thread)
			}
			worker, err := managed.New(m, factory, managed.Config{PollInterval: time.Millisecond, PermitTTL: time.Second, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- worker.Run(ctx) }()
			defer func() { cancel(); <-done }()
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				events, err := m.ListEvents(ctx, api.EventFilter{ThreadID: thread.ID, After: published.Sequence})
				if err != nil {
					t.Fatal(err)
				}
				state, err := m.GetThread(ctx, thread.ID)
				if err != nil {
					t.Fatal(err)
				}
				var failures []protocol.Event
				for _, event := range events {
					if event.Error != "" {
						failures = append(failures, event)
					}
				}
				if len(failures) == 1 && len(failures[0].MessageIDs) == 1 && failures[0].MessageIDs[0] == input.ID {
					if kind == protocol.InputCompact && state.State == api.Idle && failures[0].Kind == protocol.EventRunFailed {
						return
					}
					if kind == protocol.InputResume && state.State == api.Blocked && state.Block.CheckpointID == "missing" && failures[0].RunID == "old" {
						return
					}
				}
				time.Sleep(time.Millisecond)
			}
			t.Fatal("failure was not delivered once or thread ownership changed")
		})
	}
}
