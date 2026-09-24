package runtime_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	host "eino-cli/deepagent/host/runtime"
	"eino-cli/deepagent/protocol"
)

func TestManagedTokenEventsAccumulateAndResetAtNewRun(t *testing.T) {
	m := testManager(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		delta := map[string]any{"role": "assistant", "content": "done"}
		finish := "stop"
		if index == 1 {
			delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "read-one", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"path":"fixture.txt"}`}}}}
			finish = "tool_calls"
		}
		for _, chunk := range []map[string]any{
			{"id": "response", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": delta}}},
			{"id": "response", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}},
			{"id": "response", "object": "chat.completion.chunk", "choices": []any{}, "usage": map[string]int{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}},
		} {
			raw, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", raw)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	stop := startWorker(t, m, server.URL)
	defer stop()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "fixture.txt"), []byte("read me"), 0600); err != nil {
		t.Fatal(err)
	}
	r := host.New(m, host.Config{SessionID: protocol.NewID("session"), WorkDir: root, PollInterval: 5 * time.Millisecond})
	defer r.Detach()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var previousRun string
	for i, want := range [][]int64{{5, 10}, {5}} {
		var events []protocol.Event
		result, err := r.ExecuteEvents(ctx, "go", func(e protocol.Event) { events = append(events, e) })
		if err != nil || !result.Success {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		var totals []int64
		consumed := 0
		ids := map[string]bool{}
		for n, e := range events {
			if e.ID == "" || ids[e.ID] {
				t.Fatalf("duplicate or missing event identity: %+v", e)
			}
			ids[e.ID] = true
			if e.Kind == protocol.EventInputConsumed {
				consumed++
				if e.Text != "go" || len(e.MessageIDs) != 1 || len(totals) > 0 {
					t.Fatalf("input consumption lost identity/order: %+v", e)
				}
			}
			if e.Kind != protocol.EventTokens {
				continue
			}
			var usage struct {
				Prompt     int64 `json:"prompt_tokens"`
				Completion int64 `json:"completion_tokens"`
				Total      int64 `json:"total_tokens"`
			}
			if err := json.Unmarshal(e.Data, &usage); err != nil {
				t.Fatal(err)
			}
			totals = append(totals, usage.Total)
			if usage.Prompt+usage.Completion != usage.Total || e.Text != fmt.Sprint(usage.Total) || len(e.MessageIDs) == 0 {
				t.Fatalf("incomplete token event: %+v", e)
			}
			if n == 0 || events[n-1].Kind != protocol.EventText {
				t.Fatalf("usage reordered before assistant response: %+v", events)
			}
			if i > 0 && e.RunID == previousRun {
				t.Fatal("new input reused completed run")
			}
			if i == 0 {
				previousRun = e.RunID
			}
		}
		if fmt.Sprint(totals) != fmt.Sprint(want) {
			t.Fatalf("run=%d totals=%v want=%v events=%+v", i, totals, want, events)
		}
		if consumed != 1 {
			t.Fatalf("input consumed events=%d", consumed)
		}
		if events[len(events)-1].Kind != protocol.EventRunCompleted {
			t.Fatal("terminal event must follow all token events")
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("unexpected model calls: %d", calls.Load())
	}
}
