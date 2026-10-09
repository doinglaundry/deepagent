package conversation

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestBuildRequestPreservesHistoricalMessages(t *testing.T) {
	ctx := context.Background()
	conversation := New("thread", nil, nil, nil, 0, nil)
	original := agentmodel.NewSystemMessage("historical instruction")
	err := conversation.AddHistory(ctx, "old", original, agentmodel.NewUserMessage("prior"))
	if err != nil {
		t.Fatal(err)
	}
	request, err := conversation.BuildRequest(ctx, []*agentmodel.Message{agentmodel.NewSystemMessage("fresh prompt")})
	if err != nil {
		t.Fatal(err)
	}
	if len(request) != 3 || request[0].Content != "fresh prompt" || !reflect.DeepEqual(agentmodel.ToEino(request[1]), agentmodel.ToEino(original)) {
		t.Fatalf("request lost prompt or history: %v", request)
	}
	request[1] = agentmodel.NewSystemMessage("replacement")
	history := conversation.GetHistory(ctx)
	if len(history) != 2 || !reflect.DeepEqual(agentmodel.ToEino(history[0]), agentmodel.ToEino(original)) || original.Content != "historical instruction" {
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
	current := []*agentmodel.Message{
		agentmodel.NewUserMessage("old"), agentmodel.NewAssistantMessage("old answer", nil),
		agentmodel.NewUserMessage("new"),
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "read"}}}},
		agentmodel.NewToolMessage("result", "call"), agentmodel.NewAssistantMessage("done", nil),
	}
	summary, compactedmsgcnt, err := strategy.Summarize(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	if summary == nil || summary.Role != schema.System || compactedmsgcnt != 2 {
		t.Fatalf("summary=%+v compactedmsgcnt=%d", summary, compactedmsgcnt)
	}
	store := &testStore{}
	liveConversation := New("thread", store, strategy, nil, 0, nil)
	err = liveConversation.AddHistory(context.Background(), "run", current...)
	if err != nil {
		t.Fatal(err)
	}
	_, err = liveConversation.Compact(context.Background(), "run")
	if err != nil {
		t.Fatal(err)
	}
	err = liveConversation.AddHistory(context.Background(), "run", agentmodel.NewUserMessage("later"))
	if err != nil {
		t.Fatal(err)
	}
	restoredConversation := New("thread", store, nil, nil, 0, nil)
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
	oldSummary := agentmodel.NewSystemMessage("Earlier conversation summary:\nold goal")
	newSummary := agentmodel.NewSystemMessage("Earlier conversation summary:\nreplacement")
	original := []*agentmodel.Message{agentmodel.NewUserMessage("old"), agentmodel.NewAssistantMessage("old answer", nil), agentmodel.NewUserMessage("retain"), agentmodel.NewUserMessage("new input")}
	for _, testCase := range []struct {
		name        string
		want        []*agentmodel.Message
		wantCompact bool
		wantError   bool
	}{
		{"append", []*agentmodel.Message{oldSummary, original[2], original[3]}, true, false},
		{"another_compaction", []*agentmodel.Message{newSummary, original[3]}, false, false},
		{"reload", []*agentmodel.Message{newSummary, original[3]}, false, false},
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
			liveConversation := New("thread", store, strategy, nil, 0, nil)
			err := liveConversation.AddHistory(ctx, "run", original[:3]...)
			if err != nil {
				t.Fatal(err)
			}
			compacted := make(chan *agentmodel.ContextTokenUsage, 1)
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
				otherConversation := New("thread", store, strategy, nil, 0, nil)
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
			var payload *agentmodel.ContextTokenUsage
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
			if !reflect.DeepEqual(agentmodel.ToEinoMessages(history), agentmodel.ToEinoMessages(testCase.want)) {
				t.Fatalf("compaction overwrote current history: %+v", history)
			}
			if payload == nil && liveConversation.GetContextUsage() != beforeUsage {
				t.Fatal("uncommitted compaction changed usage")
			}
			restoredConversation := New("thread", store, nil, nil, 0, nil)
			err = restoredConversation.ReloadHistory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(agentmodel.ToEinoMessages(restoredConversation.GetHistory(context.Background())), agentmodel.ToEinoMessages(testCase.want)) {
				t.Fatal("durable context does not match merged history")
			}
			if restoredConversation.GetContextUsage() != liveConversation.GetContextUsage() {
				t.Fatal("merged context usage differs after reload")
			}
		})
	}
}

func TestNeedsCompactionUsesTokenLimit(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		compactor *SummaryCompaction
		tokens    int
		want      bool
	}{
		{"no_compactor", nil, 100, false},
		{"zero_limit", &SummaryCompaction{}, 100, false},
		{"negative_limit", &SummaryCompaction{TokenLimit: -1}, 100, false},
		{"below_limit", &SummaryCompaction{TokenLimit: 100}, 99, false},
		{"at_limit", &SummaryCompaction{TokenLimit: 100}, 100, true},
		{"above_limit", &SummaryCompaction{TokenLimit: 100}, 101, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			conversation := New("thread", nil, testCase.compactor, nil, 0, nil)
			conversation.RecordModelUsage(context.Background(), &model.TokenUsage{TotalTokens: testCase.tokens})
			needsCompaction := conversation.NeedsCompaction(context.Background())
			if needsCompaction != testCase.want {
				t.Fatalf("NeedsCompaction=%t want=%t", needsCompaction, testCase.want)
			}
		})
	}
}
