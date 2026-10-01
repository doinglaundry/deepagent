package deepagents

import (
	"context"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type usageThreadModel struct{ threadModel }

func (m *usageThreadModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (m *usageThreadModel) Stream(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.inputs = append(m.inputs, input)
	message := schema.AssistantMessage("done", nil)
	message.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func TestThread_UsageResetsForEachRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	m := &usageThreadModel{}
	events := make(chan Event, 100)
	th := newTestThread("thread", &RunConfig{Agent: Config{Model: m}}, events, ThreadOptions{})
	defer th.Close(context.Background())
	err := th.InitHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		result, err := th.SubmitInput(ctx, schema.UserMessage("go"))
		if err != nil {
			t.Fatal(err)
		}
		err = result.RunHandle.Wait(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(m.inputs) != 2 {
		t.Fatal("expected one model call per Run")
	}
	count := 0
	for len(events) > 0 {
		e := <-events
		if e.Type != EventTokens {
			continue
		}
		count++
		if e.Payload.(TokenUsagePayload).TotalTokens != 5 {
			t.Fatalf("counter leaked across runs: %+v", e.Payload)
		}
	}
	if count != 2 {
		t.Fatalf("token events=%d", count)
	}
}
