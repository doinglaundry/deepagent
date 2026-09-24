package runtime_test

import (
	"context"
	host "eino-cli/deepagent/host/runtime"
	"eino-cli/deepagent/manager/api"
	"eino-cli/deepagent/protocol"
	"encoding/json"
	"fmt"
	"github.com/cloudwego/eino/schema"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestManagedReloadRefreshesPromptAndRepairsCancelledToolRequest(t *testing.T) {
	m := testManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	thread, err := m.CreateThread(ctx, api.CreateThreadRequest{WorkDir: t.TempDir(), Input: &protocol.Input{Kind: protocol.InputUser, Text: "old"}})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := m.ClaimThread(ctx, thread.ID, "old-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	old := []*schema.Message{schema.SystemMessage("obsolete bootstrap prompt"), schema.SystemMessage("Earlier conversation summary: keep this context"), schema.UserMessage("old"), schema.AssistantMessage("", []schema.ToolCall{{ID: "interrupted-call", Type: "function", Function: schema.FunctionCall{Name: "read_file", Arguments: `{"path":"file"}`}}})}
	raw, _ := json.Marshal(old)
	if _, err = m.SaveHistory(ctx, claim.Permit, api.History{Messages: raw}); err != nil {
		t.Fatal(err)
	}
	if _, err = m.PublishEvent(ctx, claim.Permit, protocol.Event{Kind: protocol.EventRunCancelled, RunID: "old-run", MessageIDs: []string{claim.Inputs[0].ID}}); err != nil {
		t.Fatal(err)
	}
	if err = m.ReleaseThread(ctx, claim.Permit, api.Release{}); err != nil {
		t.Fatal(err)
	}
	requests := make(chan []schema.Message, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []schema.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		select {
		case requests <- request.Messages:
		default:
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"response\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"done\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"response\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	stop := startWorker(t, m, server.URL)
	defer stop()
	runtime := host.New(m, host.Config{SessionID: thread.SessionID, ThreadID: thread.ID, WorkDir: thread.WorkDir, PollInterval: 5 * time.Millisecond})
	defer runtime.Detach()
	result, err := runtime.ExecuteEvents(ctx, "continue", nil)
	if err != nil || !result.Success {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var request []schema.Message
	select {
	case request = <-requests:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if len(request) != 8 || !strings.Contains(request[0].Content, "careful coding") || request[3].Content != old[1].Content {
		t.Fatalf("stale prompt or summary lost: %+v", request)
	}
	if request[6].Role != schema.Tool || request[6].ToolCallID != "interrupted-call" || request[6].Content == "" || request[7].Content != "continue" {
		t.Fatalf("cancelled tool pair not repaired: %+v", request)
	}
	for _, message := range request {
		if message.Content == "obsolete bootstrap prompt" {
			t.Fatal("obsolete instructions reached model")
		}
	}
	stored, err := m.LoadHistory(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	var history []*schema.Message
	if err = json.Unmarshal(stored.Messages, &history); err != nil {
		t.Fatal(err)
	}
	if len(history) != 6 || history[0].Content != old[0].Content || history[1].Content != old[1].Content {
		t.Fatalf("request projection changed durable history: %v", history)
	}
	for _, message := range history {
		if message.ToolCallID == "interrupted-call" {
			t.Fatal("synthetic repair persisted as real tool result")
		}
	}
}
