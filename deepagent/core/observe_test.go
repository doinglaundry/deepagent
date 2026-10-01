package deepagents

import (
	"bytes"
	"context"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"encoding/json"
	"errors"
	"github.com/cloudwego/eino/schema"
	"io"

	"testing"
)

type transcriptWriter struct {
	bytes.Buffer
	closes int
	fail   error
}

func (w *transcriptWriter) Write(p []byte) (int, error) {
	if w.closes != 0 {
		return 0, errors.New("write after close")
	}
	if w.fail != nil {
		return 0, w.fail
	}
	return w.Buffer.Write(p)
}
func (w *transcriptWriter) Close() error { w.closes++; return nil }

func TestRun_TranscriptObservesCanonicalEvents(t *testing.T) {
	var writers []*transcriptWriter
	template := &middleware.Transcript{Open: func(_ context.Context, threadID, runID string) (io.WriteCloser, error) {
		if threadID != "thread" || runID == "" {
			t.Fatal("missing run identity")
		}
		w := &transcriptWriter{}
		writers = append(writers, w)
		return w, nil
	}}
	for range 2 {
		var delivered []types.RuntimeEvent
		m := &sequenceModel{responses: [][]*schema.Message{
			{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})},
			{schema.AssistantMessage("done", nil)},
		}}
		a, err := NewRun(context.Background(), WithConfig(&Config{Model: m, ThreadID: "thread", ToolDescriptors: []tools.ToolDescriptor{{Tool: &countingTool{}}}, Middlewares: []middleware.Middleware{template}, Emit: func(_ context.Context, e types.RuntimeEvent) error { delivered = append(delivered, e); return nil }}))
		if err != nil {
			t.Fatal(err)
		}
		{
			_, err := a.Execute(context.Background(), []*schema.Message{schema.UserMessage("go")})
			if err != nil {
				t.Fatal(err)
			}
		}
		if len(delivered) == 0 || delivered[len(delivered)-1].Kind != "turn_end" {
			t.Fatal("missing final event")
		}
		for i := range delivered {
			if delivered[i].Sequence != uint64(i+1) {
				t.Fatal("event sequence diverged")
			}
		}
		w := writers[len(writers)-1]
		if w.closes != 1 {
			t.Fatalf("close count=%d", w.closes)
		}
		decoder := json.NewDecoder(bytes.NewReader(w.Bytes()))
		var roles []string
		for {
			var record struct {
				Role    string          `json:"role"`
				Message *schema.Message `json:"message"`
			}
			{
				err := decoder.Decode(&record)
				if err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
			}
			if record.Message == nil {
				t.Fatal("missing original message")
			}
			roles = append(roles, record.Role)
		}
		if len(roles) != 4 || roles[0] != "user" || roles[1] != "assistant" || roles[2] != "tool" || roles[3] != "assistant" {
			t.Fatalf("duplicated or missing transcript messages: %v", roles)
		}
	}
	if len(writers) != 2 || writers[0] == writers[1] {
		t.Fatal("shared run writer")
	}
}

func TestRun_TranscriptWriteFailureClosesWriterAndStopsModel(t *testing.T) {
	want := errors.New("transcript disk failure")
	w := &transcriptWriter{fail: want}
	m := &sequenceModel{}
	a, err := NewRun(context.Background(), WithConfig(&Config{Model: m, Middlewares: []middleware.Middleware{&middleware.Transcript{Open: func(context.Context, string, string) (io.WriteCloser, error) { return w, nil }}}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Execute(context.Background(), []*schema.Message{schema.UserMessage("go")})
	if !errors.Is(err, want) || m.calls != 0 || w.closes != 1 {
		t.Fatalf("err=%v model=%d closes=%d", err, m.calls, w.closes)
	}
}
