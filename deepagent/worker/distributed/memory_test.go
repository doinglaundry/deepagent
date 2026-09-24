package distributed

import (
	"context"
	"eino-cli/deepagent/core/memory"
	"eino-cli/deepagent/manager/api"
	"eino-cli/deepagent/manager/compat"
	"eino-cli/deepagent/protocol"
	"encoding/json"
	"errors"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"testing"
	"time"
)

type extractModel struct{ calls int }

func (m *extractModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (m *extractModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("memory extraction bypassed Graph")
}
func (m *extractModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.calls++
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("Useful stable memory", nil)}), nil
}
func TestMemorySweepProcessesPersistedHistoryAfterRestart(t *testing.T) {
	ctx := context.Background()
	m := manager.NewMemory("memory-sweep")
	input := protocol.Input{Kind: protocol.InputUser, Text: "Remember project facts"}
	thread, err := m.CreateThread(ctx, api.CreateThreadRequest{WorkDir: t.TempDir(), Input: &input})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := m.ClaimThread(ctx, thread.ID, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal([]*schema.Message{schema.UserMessage("I use Go")})
	if _, err = m.SaveHistory(ctx, claim.Permit, api.History{Messages: data}); err != nil {
		t.Fatal(err)
	}
	if err = m.ConfirmInputDelivery(ctx, claim.Permit, claim.Inputs[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = m.PublishEvent(ctx, claim.Permit, protocol.Event{Kind: protocol.EventRunCompleted, RunID: "run", MessageIDs: []string{claim.Inputs[0].ID}}); err != nil {
		t.Fatal(err)
	}
	if err = m.ReleaseThread(ctx, claim.Permit, api.Release{}); err != nil {
		t.Fatal(err)
	}
	model := &extractModel{}
	root := t.TempDir()
	factory := func() (memory.Service, error) {
		return memory.New(memory.Config{Root: root, Model: model, Store: m, Consolidator: func(context.Context, string, string) (string, error) { return "Consolidated Go preference", nil }})
	}
	service, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	scanner := memoryScanner{sources: m, memory: service}
	if err = scanner.scan(ctx); err != nil {
		t.Fatal(err)
	}
	if model.calls != 1 {
		t.Fatalf("extractions %d", model.calls)
	}
	if err = scanner.scan(ctx); err != nil {
		t.Fatal(err)
	}
	if model.calls != 1 {
		t.Fatal("unchanged source extracted again")
	}
	pipeline, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	summary, err := pipeline.Read(ctx, thread.SessionID)
	if err != nil || summary.Summary != "Consolidated Go preference" {
		t.Fatalf("summary %+v err %v", summary, err)
	}
}
