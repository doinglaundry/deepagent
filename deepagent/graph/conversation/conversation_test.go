package conversation

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	agentmodel "eino-cli/deepagent/model"
)

type testStore struct {
	records  []*agentmodel.Message
	contexts map[*agentmodel.Message][]*agentmodel.Message
	fail     bool
}

func TestMessageIDAllocationFailureDoesNotPublishMessage(t *testing.T) {
	failure := errors.New("id allocation failed")
	conversation := New("thread", &testStore{}, nil, nil, 0, func(context.Context, *agentmodel.Message) (string, error) { return "", failure })
	err := conversation.AddHistory(context.Background(), "run", agentmodel.NewUserMessage("must not appear"))
	if !errors.Is(err, failure) || len(conversation.GetHistory(context.Background())) != 0 {
		t.Fatalf("allocation failure was swallowed: %v", err)
	}
}
func (store *testStore) AppendMessage(_ context.Context, message *agentmodel.Message) error {
	if store.fail {
		return errors.New("store failed")
	}
	for _, record := range store.records {
		if message.MessageID != "" && record.ThreadID == message.ThreadID && record.MessageID == message.MessageID {
			message.Seq = record.Seq
			return nil
		}
	}
	message.Seq = int64(len(store.records) + 1)
	if message.MessageID == "" {
		message.MessageID = fmt.Sprint(message.Seq)
	}
	copy := *message
	store.records = append(store.records, &copy)
	return nil
}
func (store *testStore) SaveContext(ctx context.Context, messages []*agentmodel.Message) error {
	summary := messages[0]
	err := store.AppendMessage(ctx, summary)
	if err != nil {
		return err
	}
	if store.contexts == nil {
		store.contexts = make(map[*agentmodel.Message][]*agentmodel.Message)
	}
	store.contexts[store.records[len(store.records)-1]] = slices.Clone(messages)
	return nil
}
func (store *testStore) LoadContext(_ context.Context, threadID string) (messages []*agentmodel.Message, recordedMessageIDs []string, sequence int64, err error) {
	for _, record := range store.records {
		if record.ThreadID != threadID {
			continue
		}
		copy := *record
		sequence = record.Seq
		recordedMessageIDs = append(recordedMessageIDs, record.MessageID)
		context, compacted := store.contexts[record]
		if compacted {
			messages = slices.Clone(context)
		} else {
			messages = append(messages, &copy)
		}
	}
	return messages, recordedMessageIDs, sequence, nil
}
func TestContext_PersistFailureDoesNotChangeHistory(t *testing.T) {
	ctx := context.Background()
	store := &testStore{fail: true}
	conversation := New("thread", store, nil, nil, 0, nil)
	err := conversation.AddHistory(ctx, "run", agentmodel.NewUserMessage("hello"))
	if err == nil {
		t.Fatal("expected persistence failure")
	}
	if len(conversation.GetHistory(ctx)) != 0 {
		t.Fatal("failed write appeared in history")
	}
}

func TestContext_ReloadEqualsCompactedContext(t *testing.T) {
	ctx := context.Background()
	store := &testStore{}
	summaryText := "Earlier conversation summary:\ngoal and decision"
	messageIDs := map[string]string{"older": "1", "retain": "2", summaryText: "3", "later": "4"}
	conversation := New("thread", store, &SummaryCompaction{Model: summaryModel{}, KeepRecent: 1}, nil, 0, func(_ context.Context, message *agentmodel.Message) (string, error) {
		if message == nil {
			return "", errors.New("identity requires a message")
		}
		return messageIDs[message.Content], nil
	})
	err := conversation.AddHistory(ctx, "run", agentmodel.NewUserMessage("older"), agentmodel.NewUserMessage("retain"))
	if err != nil {
		t.Fatal(err)
	}
	_, compactErr := conversation.Compact(ctx, "run")
	if compactErr != nil {
		t.Fatal(compactErr)
	}
	addHistoryErr := conversation.AddHistory(ctx, "run", agentmodel.NewUserMessage("later"))
	if addHistoryErr != nil {
		t.Fatal(addHistoryErr)
	}
	err = conversation.AddHistory(ctx, "run", agentmodel.NewUserMessage("older"))
	if err != nil {
		t.Fatal(err)
	}
	restored := New("thread", store, nil, nil, 0, nil)
	reloadHistoryErr := restored.ReloadHistory(ctx)
	if reloadHistoryErr != nil {
		t.Fatal(reloadHistoryErr)
	}
	messages := restored.GetHistory(ctx)
	if len(messages) != 3 || messages[0].Content != summaryText || messages[1].Content != "retain" || messages[2].Content != "later" {
		t.Fatalf("retained context lost: %v", messages)
	}
}

func TestHistoryRedeliveryUsesDurableMessageIdentity(t *testing.T) {
	store := &testStore{}
	generateMessageID := func(context.Context, *agentmodel.Message) (string, error) { return "42", nil }
	first := New("thread-1", store, nil, nil, 0, generateMessageID)
	err := first.AddHistory(context.Background(), "run-1", agentmodel.NewUserMessage("once"))
	if err != nil {
		t.Fatal(err)
	}

	second := New("thread-1", store, nil, nil, 0, generateMessageID)
	reloadHistoryErr := second.ReloadHistory(context.Background())
	if reloadHistoryErr != nil {
		t.Fatal(reloadHistoryErr)
	}
	addHistoryErr := second.AddHistory(context.Background(), "run-2", agentmodel.NewUserMessage("once"))
	if addHistoryErr != nil {
		t.Fatal(addHistoryErr)
	}
	history := second.GetHistory(context.Background())
	if len(history) != 1 || history[0].Content != "once" {
		t.Fatalf("redelivery duplicated model history: %+v", history)
	}
	if len(store.records) != 1 {
		t.Fatalf("redelivery duplicated durable history: %d records", len(store.records))
	}
}

func TestReloadHistoryCrossesPageBoundary(t *testing.T) {
	store := &testStore{}
	first := New("thread", store, nil, nil, 0, nil)
	messages := make([]*agentmodel.Message, 205)
	for index := range messages {
		messages[index] = agentmodel.NewUserMessage(fmt.Sprintf("message %d", index))
	}
	err := first.AddHistory(context.Background(), "run", messages...)
	if err != nil {
		t.Fatal(err)
	}
	restored := New("thread", store, nil, nil, 0, nil)
	err = restored.ReloadHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	history := restored.GetHistory(context.Background())
	historySeq, _ := restored.SnapshotContext()
	if len(history) != 205 || history[199].Content != "message 199" || history[204].Content != "message 204" || historySeq != 205 {
		t.Fatal("reload lost messages or sequence at the page boundary")
	}
}

func TestAddHistoryDoesNotMutateInputMessage(t *testing.T) {
	input := agentmodel.NewUserMessage("original")
	history := New("thread", nil, nil, nil, 0, func(context.Context, *agentmodel.Message) (string, error) { return "42", nil })
	err := history.AddHistory(context.Background(), "run", input)
	if err != nil {
		t.Fatal(err)
	}
	if input.MessageID != "" || input.ThreadID != "" || input.RunID != "" || input.CreatedAt != 0 || input.Seq != 0 {
		t.Fatalf("history mutated caller-owned input: %+v", input)
	}
	stored := history.GetHistory(context.Background())[0]
	if stored.MessageID != "42" || stored.ThreadID != "thread" || stored.RunID != "run" || stored.CreatedAt == 0 {
		t.Fatalf("stored message lost identity: %+v", stored)
	}
}
