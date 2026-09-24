package graph

import (
	"context"
	"eino-cli/deepagent/core/internal/conversation"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/protocol"
	"encoding/json"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"os"
	"testing"
)

func TestCheckpoint_LegacyPendingInputsSurviveSecondInterrupt(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "return direct"}[direct], func(t *testing.T) {
			ctx := context.Background()
			raw, err := os.ReadFile("../runtime/checkpointer/testdata/legacy_engine_approval.json")
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]json.RawMessage
			if err = json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			pending := []protocol.Input{
				{ID: "pending-media", ThreadID: "thread", Kind: protocol.InputUser, Text: "inspect", Parts: []protocol.Part{
					{Type: "text", Text: "these"}, {Type: "image_url", URL: "https://example.test/image", MIMEType: "image/png"},
					{Type: "audio", URL: "https://example.test/audio", MIMEType: "audio/wav"}, {Type: "video", URL: "https://example.test/video", MIMEType: "video/mp4"}, {Type: "file", URL: "https://example.test/file", MIMEType: "application/pdf"},
				}},
				{ID: "pending-text", ThreadID: "thread", Kind: protocol.InputUser, Text: "then summarize"},
			}
			pending = append(pending, pending[0]) // Redelivery must retain one identity.
			wire["pending"], _ = json.Marshal(pending)
			raw, _ = json.Marshal(wire)
			var messages []*schema.Message
			if err = json.Unmarshal(wire["messages"], &messages); err != nil {
				t.Fatal(err)
			}
			history := conversation.New("thread", nil, nil, nil)
			if err = history.AddHistory(ctx, "run", messages...); err != nil {
				t.Fatal(err)
			}
			store := &checkpointMemory{values: map[string][]byte{"fixture": raw}}
			counter := &countingTool{}
			m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("done", nil)}}}
			cfg := Config{ThreadID: "thread", RunID: "run", Model: m, Conversation: history, CheckpointStore: store, ToolDescriptors: []tools.Descriptor{{Tool: counter, RequiresApproval: true, ReturnDirect: direct}}, InterruptAfterNodes: []string{"tools"}}
			first, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			_, err = first.Run(ctx, nil, WithCheckpointID("fixture"), WithResume("gate"), WithResumeData(map[string]any{"gate": &tools.ApprovalResult{CallID: "blocked", Approved: true}}))
			if _, ok := compose.ExtractInterruptInfo(err); !ok {
				t.Fatalf("expected second interrupt: %v", err)
			}
			if err = first.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if counter.count.Load() != 1 || m.calls != 0 || len(history.History(ctx)) != 4 {
				t.Fatal("pending input consumed before tool boundary")
			}
			cfg.InterruptAfterNodes = nil
			second, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer second.Close(ctx)
			result, err := second.Run(ctx, nil, WithCheckpointID("fixture"))
			if err != nil {
				t.Fatal(err)
			}
			if result.Content != "done" || counter.count.Load() != 1 || m.calls != 1 || len(m.inputs[0]) != 6 || len(second.state.Consumed) != 3 {
				t.Fatalf("result=%v calls=%d model=%d input=%v consumed=%v", result, counter.count.Load(), m.calls, m.inputs, second.state.Consumed)
			}
			media := m.inputs[0][4]
			if media.Content != "inspect\nthese" || media.Extra["message_id"] != "pending-media" || m.inputs[0][5].Extra["message_id"] != "pending-text" || len(media.UserInputMultiContent) != 6 {
				t.Fatalf("pending identity or multimodal content lost: %+v", media)
			}
			parts := media.UserInputMultiContent
			if *parts[2].Image.URL != pending[0].Parts[1].URL || parts[2].Image.MIMEType != "image/png" || *parts[3].Audio.URL != pending[0].Parts[2].URL || *parts[4].Video.URL != pending[0].Parts[3].URL || *parts[5].File.URL != pending[0].Parts[4].URL {
				t.Fatal("media URLs or MIME lost")
			}
			if _, exists := second.state.Extensions["legacy_pending_inputs"]; exists {
				t.Fatal("pending marker not retired")
			}

		})
	}
}
