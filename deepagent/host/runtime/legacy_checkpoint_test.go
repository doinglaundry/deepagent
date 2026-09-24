package runtime_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"eino-cli/deepagent/core/runtime/checkpointer"
	host "eino-cli/deepagent/host/runtime"
	"eino-cli/deepagent/manager/api"
	"eino-cli/deepagent/protocol"
	"github.com/cloudwego/eino/schema"
)

func TestManagedLegacyCheckpointRestoresPendingInputWithoutRepeatingWrite(t *testing.T) {
	for _, approved := range []bool{true, false} {
		t.Run(map[bool]string{true: "approved", false: "denied"}[approved], func(t *testing.T) {
			m := testManager(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			dir := t.TempDir()
			initial := protocol.Input{Kind: protocol.InputUser, Text: "go"}
			thread, err := m.CreateThread(ctx, api.CreateThreadRequest{WorkDir: dir, Input: &initial})
			if err != nil {
				t.Fatal(err)
			}
			claim, err := m.ClaimThread(ctx, thread.ID, "legacy-worker", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			original := claim.Inputs[0]
			if err = m.ConfirmInputDelivery(ctx, claim.Permit, original.ID); err != nil {
				t.Fatal(err)
			}
			pending, err := m.SubmitInput(ctx, thread.ID, protocol.Input{Kind: protocol.InputUser, Text: "also summarize the result"})
			if err != nil {
				t.Fatal(err)
			}
			if err = m.ConfirmInputDelivery(ctx, claim.Permit, pending.ID); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile("../../core/runtime/checkpointer/testdata/legacy_engine_approval.json")
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]json.RawMessage
			if err = json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			runID, cpID := protocol.NewID("legacy-run"), protocol.NewID("legacy-checkpoint")
			var messages []*schema.Message
			if err = json.Unmarshal(wire["messages"], &messages); err != nil {
				t.Fatal(err)
			}
			messages[0].Extra["deepagent_input_id"] = original.ID
			calls := []schema.ToolCall{
				{ID: "completed", Type: "function", Function: schema.FunctionCall{Name: "write_file", Arguments: `{"path":"already.txt","content":"must not overwrite completed write"}`}},
				{ID: "blocked", Type: "function", Function: schema.FunctionCall{Name: "write_file", Arguments: `{"path":"restored.txt","content":"approved content"}`}},
			}
			messages[1].ToolCalls = calls
			messages[2].Name = "write_file"
			block := &protocol.Block{Kind: "approval", RunID: runID, CheckpointID: cpID, InterruptID: "legacy-gate", ToolName: "write_file", Arguments: calls[1].Function.Arguments, Question: "Allow write_file?"}
			fields := map[string]any{"thread_id": thread.ID, "run_id": runID, "namespace": thread.Namespace, "checkpoint_id": cpID, "messages": messages, "message_ids": []string{original.ID}, "pending": []protocol.Input{pending}, "calls": calls, "block": block}
			for key, value := range fields {
				wire[key], err = json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
			}
			raw, err = json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, "already.txt"), []byte("completed once"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = m.SaveHistory(ctx, claim.Permit, api.History{Messages: wire["messages"]}); err != nil {
				t.Fatal(err)
			}
			if err = m.PutCheckpoint(ctx, cpID, raw); err != nil {
				t.Fatal(err)
			}
			if _, err = m.PublishEvent(ctx, claim.Permit, protocol.Event{Kind: protocol.EventBlocked, RunID: runID, MessageIDs: []string{original.ID}, Block: block}); err != nil {
				t.Fatal(err)
			}
			if err = m.ReleaseThread(ctx, claim.Permit, api.Release{Block: block}); err != nil {
				t.Fatal(err)
			}
			server, modelCalls := fakeModelServer(t)
			startWorker(t, m, server.URL)
			resumed := host.New(m, host.Config{SessionID: thread.SessionID, ThreadID: thread.ID, WorkDir: dir, PollInterval: 5 * time.Millisecond})
			defer resumed.Detach()
			var final protocol.Event
			result, err := resumed.ExecuteEvents(ctx, map[bool]string{true: "yes", false: "no"}[approved], func(event protocol.Event) {
				if event.Kind == protocol.EventRunCompleted {
					final = event
				}
			})
			if err != nil || !result.Success {
				t.Fatalf("legacy resume: result=%+v err=%v", result, err)
			}
			if final.RunID != runID || !slices.Contains(final.MessageIDs, original.ID) || !slices.Contains(final.MessageIDs, pending.ID) {
				t.Fatalf("run/input ownership lost: %+v", final)
			}
			if modelCalls.Load() != 1 {
				t.Fatalf("pre-approval model decision repeated: %d", modelCalls.Load())
			}
			completed, err := os.ReadFile(filepath.Join(dir, "already.txt"))
			if err != nil || string(completed) != "completed once" {
				t.Fatalf("completed write replayed: %q %v", completed, err)
			}
			restored, err := os.ReadFile(filepath.Join(dir, "restored.txt"))
			if (approved && (err != nil || string(restored) != "approved content")) || (!approved && !os.IsNotExist(err)) {
				t.Fatalf("approved tool not executed: %q %v", restored, err)
			}
			stored, err := m.LoadHistory(ctx, thread.ID)
			if err != nil {
				t.Fatal(err)
			}
			var history []*schema.Message
			if err = json.Unmarshal(stored.Messages, &history); err != nil {
				t.Fatal(err)
			}
			var pendingCount, completedCount int
			for _, message := range history {
				if message.Extra["message_id"] == pending.ID {
					pendingCount++
				}
				if message.ToolCallID == "completed" {
					completedCount++
				}
			}
			if pendingCount != 1 || completedCount != 1 || len(history) != 6 || len(stored.Rollout) == 0 {
				t.Fatalf("history duplicated or lost: pending=%d completed=%d messages=%v", pendingCount, completedCount, history)
			}
			migrated, err := m.GetCheckpoint(ctx, cpID)
			if err != nil {
				t.Fatal(err)
			}
			var envelope checkpointer.Envelope
			if err = json.Unmarshal(migrated, &envelope); err != nil || envelope.Version != 1 || envelope.ThreadID != thread.ID || envelope.RunID != runID {
				t.Fatalf("checkpoint migration not persisted: %+v %v", envelope, err)
			}

		})
	}
}
