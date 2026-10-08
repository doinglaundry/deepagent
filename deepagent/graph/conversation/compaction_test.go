package conversation

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	messagepkg "eino-cli/deepagent/message"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestBuildRequestPreservesHistoricalMessages(t *testing.T) {
	ctx := context.Background()
	conversation := New("thread", nil, nil, nil)
	original := messagepkg.NewSystemMessage("historical instruction")
	err := conversation.AddHistory(ctx, "old", original, messagepkg.NewUserMessage("prior"))
	if err != nil {
		t.Fatal(err)
	}
	request, err := conversation.BuildRequest(ctx, []*messagepkg.Message{messagepkg.NewSystemMessage("fresh prompt")})
	if err != nil {
		t.Fatal(err)
	}
	if len(request) != 3 || request[0].Content != "fresh prompt" || !reflect.DeepEqual(messagepkg.ToEino(request[1]), messagepkg.ToEino(original)) {
		t.Fatalf("request lost prompt or history: %v", request)
	}
	request[1] = messagepkg.NewSystemMessage("replacement")
	history := conversation.GetHistory(ctx)
	if len(history) != 2 || !reflect.DeepEqual(messagepkg.ToEino(history[0]), messagepkg.ToEino(original)) || original.Content != "historical instruction" {
		t.Fatal("request projection mutated source history")
	}
}

type summaryModel struct {
	generate func(context.Context, []*schema.Message) (*schema.Message, error)
}

func (summaryModel summaryModel) Generate(ctx context.Context, messages []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if summaryModel.generate != nil {
		return summaryModel.generate(ctx, messages)
	}
	return schema.AssistantMessage("goal and decision", nil), nil
}
func (summaryModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("unused")
}

func TestSummaryCompactionPreservesRecentToolExchange(t *testing.T) {
	strategy := &SummaryCompaction{Model: summaryModel{}, KeepRecent: 3, TokenLimit: 100}
	current := []*messagepkg.Message{
		messagepkg.NewUserMessage("old"), messagepkg.NewAssistantMessage("old answer", nil),
		messagepkg.NewUserMessage("new"),
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "read"}}}},
		messagepkg.NewToolMessage("result", "call"), messagepkg.NewAssistantMessage("done", nil),
	}
	summary, compactedCount, err := strategy.Compact(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	if summary == nil || summary.Role != schema.System || compactedCount != 2 {
		t.Fatalf("summary=%+v compactedCount=%d", summary, compactedCount)
	}
	store := &testStore{}
	liveConversation := New("thread", store, strategy, nil)
	err = liveConversation.AddHistory(context.Background(), "run", current...)
	if err != nil {
		t.Fatal(err)
	}
	_, err = liveConversation.Compact(context.Background(), "run")
	if err != nil {
		t.Fatal(err)
	}
	err = liveConversation.AddHistory(context.Background(), "run", messagepkg.NewUserMessage("later"))
	if err != nil {
		t.Fatal(err)
	}
	restoredConversation := New("thread", store, nil, nil)
	err = restoredConversation.ReloadHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	restoredHistory := restoredConversation.GetHistory(context.Background())
	if len(restoredHistory) != 6 || restoredHistory[2].ToolCalls[0].ID != "call" || restoredHistory[3].ToolCallID != "call" || restoredHistory[5].Content != "later" {
		t.Fatalf("durable reload lost retained exchange: %+v", restoredHistory)
	}
}

func TestCompactionPreservesConcurrentHistory(t *testing.T) {
	oldSummary := messagepkg.NewSystemMessage("Earlier conversation summary:\nold goal")
	newSummary := messagepkg.NewSystemMessage("Earlier conversation summary:\nreplacement")
	original := []*messagepkg.Message{messagepkg.NewUserMessage("old"), messagepkg.NewAssistantMessage("old answer", nil), messagepkg.NewUserMessage("retain"), messagepkg.NewUserMessage("new input")}
	for _, testCase := range []struct {
		name        string
		want        []*messagepkg.Message
		wantCompact bool
		wantError   bool
	}{
		{"append", []*messagepkg.Message{oldSummary, original[2], original[3]}, true, false},
		{"another_compaction", []*messagepkg.Message{newSummary, original[3]}, false, false},
		{"reload", []*messagepkg.Message{newSummary, original[3]}, false, false},
		{"store_failure", original, false, true},
		{"cancel", original, false, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			started := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			finishSummary := func() { releaseOnce.Do(func() { close(release) }) }
			defer finishSummary()
			var modelCalls atomic.Int32
			strategy := &SummaryCompaction{KeepRecent: 1, Model: summaryModel{generate: func(ctx context.Context, messages []*schema.Message) (*schema.Message, error) {
				if modelCalls.Add(1) > 1 {
					return schema.AssistantMessage("replacement", nil), nil
				}
				close(started)
				select {
				case <-release:
					return schema.AssistantMessage("old goal", nil), nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}}}
			store := &testStore{}
			liveConversation := New("thread", store, strategy, nil)
			err := liveConversation.AddHistory(ctx, "run", original[:3]...)
			if err != nil {
				t.Fatal(err)
			}
			compacted := make(chan *ContextCompactedPayload, 1)
			failed := make(chan error, 1)
			go func() {
				payload, err := liveConversation.Compact(ctx, "run")
				compacted <- payload
				failed <- err
			}()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			err = liveConversation.AddHistory(ctx, "run", original[3])
			if err != nil {
				t.Fatal(err)
			}
			switch testCase.name {
			case "another_compaction":
				_, err = liveConversation.Compact(ctx, "winner")
			case "reload":
				otherConversation := New("thread", store, strategy, nil)
				err = otherConversation.ReloadHistory(ctx)
				if err == nil {
					_, err = otherConversation.Compact(ctx, "winner")
				}
				if err == nil {
					err = liveConversation.ReloadHistory(ctx)
				}
			case "store_failure":
				store.fail = true
			case "cancel":
				cancel()
			}
			if err != nil {
				t.Fatal(err)
			}
			beforeUsage := liveConversation.GetContextUsage()
			finishSummary()
			var payload *ContextCompactedPayload
			select {
			case payload = <-compacted:
			case <-time.After(5 * time.Second):
				t.Fatal("compaction did not finish")
			}
			err = <-failed
			if (err != nil) != testCase.wantError || (payload != nil) != testCase.wantCompact {
				t.Fatalf("unexpected compaction result: payload=%+v err=%v", payload, err)
			}
			if testCase.name == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("compaction swallowed cancellation:", err)
			}
			history := liveConversation.GetHistory(context.Background())
			if !reflect.DeepEqual(messagepkg.ToEinoMessages(history), messagepkg.ToEinoMessages(testCase.want)) {
				t.Fatalf("compaction overwrote current history: %+v", history)
			}
			if payload == nil && liveConversation.GetContextUsage() != beforeUsage {
				t.Fatal("uncommitted compaction changed usage")
			}
			restoredConversation := New("thread", store, nil, nil)
			err = restoredConversation.ReloadHistory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(messagepkg.ToEinoMessages(restoredConversation.GetHistory(context.Background())), messagepkg.ToEinoMessages(testCase.want)) {
				t.Fatal("durable context does not match merged history")
			}
			if restoredConversation.GetContextUsage() != liveConversation.GetContextUsage() {
				t.Fatal("merged context usage differs after reload")
			}
		})
	}
}
