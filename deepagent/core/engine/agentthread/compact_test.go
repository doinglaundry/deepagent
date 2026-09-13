package agentthread

import (
	"context"
	"eino-cli/deepagent/core/compact"
	"eino-cli/protocol"
	"errors"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"testing"
)

type slowSummary struct{ entered, release chan struct{} }

func (s slowSummary) Generate(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	close(s.entered)
	select {
	case <-s.release:
		return schema.AssistantMessage("stale summary", nil), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (s slowSummary) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("unused")
}
func TestStaleCompactionCannotOverwriteNewHistory(t *testing.T) {
	store := &memoryStore{}
	for i := 0; i < 6; i++ {
		store.history = append(store.history, schema.UserMessage("old"), schema.AssistantMessage("reply", nil))
	}
	summary := slowSummary{make(chan struct{}), make(chan struct{})}
	f := &fakeModel{stream: func(context.Context, int, []*schema.Message) *schema.StreamReader[*schema.Message] {
		return chunks(schema.AssistantMessage("fresh response", nil))
	}}
	th := newThread(t, Config{Model: f, SummaryModel: summary, History: store})
	result := make(chan error, 1)
	go func() { result <- th.Compact(context.Background()) }()
	<-summary.entered
	th.SubmitInput(context.Background(), input("fresh user"))
	terminal(t, th)
	close(summary.release)
	if e := <-result; !errors.Is(e, compact.ErrStale) {
		t.Fatal(e)
	}
	h, _ := store.Load(context.Background())
	if h[len(h)-1].Content != "fresh response" {
		t.Fatal("stale summary overwrote fresh history")
	}
}

type fixedSummary struct{}

func (fixedSummary) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("retained summary", nil), nil
}
func (fixedSummary) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("unused")
}
func TestAutomaticThresholdCompactionRetainsRecentInput(t *testing.T) {
	store := &memoryStore{}
	for i := 0; i < 8; i++ {
		store.history = append(store.history, schema.UserMessage("old user long repeated content"), schema.AssistantMessage("old answer long repeated content", nil))
	}
	f := &fakeModel{stream: func(context.Context, int, []*schema.Message) *schema.StreamReader[*schema.Message] {
		return chunks(schema.AssistantMessage("done", nil))
	}}
	th := newThread(t, Config{Model: f, SummaryModel: fixedSummary{}, History: store, CompactThresholdTokens: 1, KeepRecentMessages: 4})
	th.SubmitInput(context.Background(), input("newest"))
	es := terminal(t, th)
	compacted := false
	for _, e := range es {
		if e.Kind == protocol.EventCompacted {
			compacted = true
		}
	}
	m := f.inputs[0]
	if !compacted || len(m) > 7 || m[0].Content != "Earlier conversation summary:\nretained summary" || m[len(m)-1].Content != "newest" {
		t.Fatal("automatic compaction lost context", m)
	}
}
