package deepagents

import (
	"context"
	"testing"
	"time"

	"eino-cli/deepagent/core/internal/conversation"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Embedding only the retained public interface models an external context
// implementation without the new graph's cumulative-usage methods.
type externalContext struct {
	ContextManager
	recorded int
}

func (c *externalContext) RecordModelUsage(ctx context.Context, usage *model.TokenUsage) {
	c.recorded++
	c.ContextManager.RecordModelUsage(ctx, usage)
}
func (c *externalContext) BuildRequest(ctx context.Context, _ []*schema.Message) ([]*schema.Message, error) {
	return append([]*schema.Message{schema.SystemMessage("external context prompt")}, c.History(ctx)...), nil
}

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

func TestThread_LegacyContextManagerRetainsUsageHookAndRequestBuilder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	legacy := &externalContext{ContextManager: conversation.New("thread", nil, nil, nil)}
	m := &usageThreadModel{}
	events := make(chan Event, 100)
	th := newTestThread("thread", &RunConfig{Agent: Config{Model: m}}, events, ThreadOptions{ContextManager: legacy})
	{
		err := th.InitHistory(ctx)
		if err != nil {
			t.Fatal(err)
		}
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
	if legacy.recorded != 2 || len(m.inputs) != 2 {
		t.Fatal("external context hook bypassed")
	}
	for _, input := range m.inputs {
		if input[0].Content != "external context prompt" {
			t.Fatal("custom request builder lost")
		}
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
