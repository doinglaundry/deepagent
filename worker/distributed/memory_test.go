package distributed

import (
	"context"
	"eino-cli/deepagent/core/memory"
	"eino-cli/manager"
	"eino-cli/manager/api"
	"eino-cli/protocol"
	"encoding/json"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"testing"
	"time"
)

type extractModel struct{ calls int }

func (m *extractModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	m.calls++
	return schema.AssistantMessage("Useful stable memory", nil), nil
}
func (m *extractModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, nil
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
	factory := func(scope string) (*memory.Pipeline, error) {
		return memory.New(memory.Config{Root: root, Model: model, Store: m, Scope: scope, Consolidator: func(context.Context, string, string) (string, error) { return "Consolidated Go preference", nil }})
	}
	scanner := memoryScanner{sources: m, pipeline: factory}
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
	pipeline, err := factory(thread.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := pipeline.Read(ctx)
	if err != nil || summary != "Consolidated Go preference" {
		t.Fatalf("summary %s err %v", summary, err)
	}
}
