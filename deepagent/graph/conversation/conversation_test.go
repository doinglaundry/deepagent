package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	gormschema "gorm.io/gorm/schema"
)

type testStore struct {
	records []*HistoryRecord
	fail    bool
}

func TestHistoryRecordHasDirectJSONMapping(t *testing.T) {
	mapping, err := gormschema.Parse(&HistoryRecord{}, &sync.Map{}, gormschema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	message := mapping.FieldsByName["Message"]
	if message == nil || message.DBName != "message" || message.Serializer == nil {
		t.Fatal("HistoryRecord must directly serialize the original Eino message into the message column")
	}
	compactedMessages := mapping.FieldsByName["CompactedMessages"]
	if compactedMessages == nil || compactedMessages.DBName != "ext" || compactedMessages.Serializer == nil {
		t.Fatal("HistoryRecord must directly serialize the complete compacted window into ext")
	}
}

type historySequence int64

func (sequence *historySequence) GenerateSequence(context.Context, string) (int64, error) {
	*sequence = *sequence + 1
	return int64(*sequence), nil
}

func TestGormHistoryMigrationAndRoundTrip(t *testing.T) {
	dsn := os.Getenv("DEEPAGENT_TEST_MYSQL_DSN")
	configPath := os.Getenv("DEEPAGENT_TEST_CONFIG")
	if dsn == "" && configPath != "" {
		raw, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			Manager struct {
				MySQLDSN string `yaml:"mysql_dsn"`
			} `yaml:"manager"`
		}
		err = yaml.Unmarshal([]byte(os.ExpandEnv(string(raw))), &config)
		if err != nil {
			t.Fatal(err)
		}
		dsn = config.Manager.MySQLDSN
	}
	if dsn == "" {
		t.Skip("set DEEPAGENT_TEST_MYSQL_DSN or DEEPAGENT_TEST_CONFIG for real history storage validation")
	}
	database, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	table := "conversation_refactor_" + uuid.NewString()[:8]
	sequence := historySequence(3)
	store := NewGormHistoryStore(database, table, &sequence)
	oldSchema := struct {
		ThreadID  string `gorm:"column:thread_id;primaryKey;size:128"`
		MessageID int64  `gorm:"column:message_id;primaryKey"`
		Seq       int64  `gorm:"column:seq"`
		RunID     string `gorm:"column:turn_id;size:128"`
		Type      string `gorm:"column:type;size:32"`
		Message   string `gorm:"column:message"`
		Ext       string `gorm:"column:ext"`
		CreateAt  int64  `gorm:"column:created_at"`
	}{}
	err = database.Table(table).AutoMigrate(&oldSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Migrator().DropTable(table) })
	summary := schema.SystemMessage("earlier goal")
	toolCall := schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`}}})
	toolResult := schema.ToolMessage("found code", "call")
	for index, retained := range [][]*schema.Message{{toolCall, toolResult}, nil} {
		snapshot, marshalErr := json.Marshal(map[string]any{"Version": 1, "Summary": summary, "Retained": retained, "SourceVersion": 9, "CoveredSeq": 2})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		extension, marshalErr := json.Marshal(map[string]string{"CompactStrategyID": "core_snapshot_v1", "CompactStrategyPayload": string(snapshot)})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		err = database.Table(table).Create(map[string]any{"thread_id": "thread", "message_id": 10 + index, "seq": index + 1, "turn_id": "run", "type": "compact", "ext": string(extension), "created_at": 42}).Error
		if err != nil {
			t.Fatal(err)
		}
	}
	ordinary, err := json.Marshal(schema.UserMessage("untouched"))
	if err != nil {
		t.Fatal(err)
	}
	err = database.Table(table).Create(map[string]any{"thread_id": "other-thread", "message_id": 90, "seq": 1, "turn_id": "old-run", "type": "message", "message": string(ordinary), "ext": "", "created_at": 42}).Error
	if err != nil {
		t.Fatal(err)
	}
	err = store.MigrateSchema(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = store.MigrateSchema(context.Background())
	if err != nil {
		t.Fatal("migration must be idempotent:", err)
	}
	records, err := store.LoadAfter(context.Background(), "thread", 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || len(records[0].CompactedMessages) != 3 || records[0].CompactedMessages[2].ToolCallID != "call" || len(records[1].CompactedMessages) != 1 {
		t.Fatalf("migration lost summary, tool exchange or empty retained window: %+v", records)
	}
	otherRecords, err := store.LoadAfter(context.Background(), "other-thread", 0, 200)
	if err != nil || len(otherRecords) != 1 || otherRecords[0].Message.Content != "untouched" {
		t.Fatalf("migration changed ordinary messages or crossed threads: %+v %v", otherRecords, err)
	}
	input := schema.UserMessage("next")
	input.Extra = map[string]any{"source": "round-trip"}
	err = store.Append(context.Background(), &HistoryRecord{ThreadID: "thread", RunID: "run", MessageID: 3, Type: HistoryRecordMessage, Message: input})
	if err != nil {
		t.Fatal(err)
	}
	conversation := New("thread", store, nil, nil)
	err = conversation.ReloadHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	history := conversation.GetHistory(context.Background())
	if len(history) != 2 || history[0].Content != "earlier goal" || history[1].Content != "next" || history[1].Extra["source"] != "round-trip" {
		t.Fatalf("reload lost migrated context or message metadata: %+v", history)
	}
	duplicate := &HistoryRecord{ThreadID: "thread", RunID: "other-run", MessageID: 3, Type: HistoryRecordMessage, Message: schema.UserMessage("must not overwrite")}
	err = store.Append(context.Background(), duplicate)
	if err != nil {
		t.Fatal(err)
	}
	records, err = store.LoadAfter(context.Background(), "thread", 2, 1)
	if err != nil || len(records) != 1 || records[0].Message.Content != "next" || duplicate.Seq != records[0].Seq {
		t.Fatalf("redelivery changed content or durable sequence: records=%+v duplicate=%+v err=%v", records, duplicate, err)
	}
	driverConfig, err := mysqlDriver.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	driverConfig.ClientFoundRows = true
	foundRowsDatabase, err := gorm.Open(mysql.Open(driverConfig.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	foundRowsSQL, err := foundRowsDatabase.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = foundRowsSQL.Close() })
	foundRowsStore := NewGormHistoryStore(foundRowsDatabase, table, &sequence)
	duplicate.Seq = 0
	err = foundRowsStore.Append(context.Background(), duplicate)
	if err != nil || duplicate.Seq != records[0].Seq {
		t.Fatalf("clientFoundRows redelivery returned an unpersisted sequence: got=%d want=%d err=%v", duplicate.Seq, records[0].Seq, err)
	}
}

func TestHistoryIDAllocationFailureDoesNotPublishMessage(t *testing.T) {
	failure := errors.New("id allocation failed")
	conversation := New("thread", &testStore{}, nil, nil, WithRecordID(func(context.Context, string, string, *schema.Message) (int64, error) { return 0, failure }))
	err := conversation.AddHistory(context.Background(), "run", schema.UserMessage("must not appear"))
	if !errors.Is(err, failure) || len(conversation.GetHistory(context.Background())) != 0 {
		t.Fatalf("allocation failure was swallowed: %v", err)
	}
}

func (historyStore *testStore) Append(_ context.Context, historyRecord *HistoryRecord) error {
	if historyStore.fail {
		return errors.New("store failed")
	}
	historyRecord.Seq = int64(len(historyStore.records) + 1)
	historyStore.records = append(historyStore.records, historyRecord)
	return nil
}
func (historyStore *testStore) LoadAfter(_ context.Context, threadID string, sequence int64, limit int) ([]*HistoryRecord, error) {
	var out []*HistoryRecord
	for _, historyRecord := range historyStore.records {
		if historyRecord.ThreadID != threadID || historyRecord.Seq <= sequence {
			continue
		}
		out = append(out, historyRecord)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}
func TestContext_PersistFailureDoesNotChangeHistory(t *testing.T) {
	ctx := context.Background()
	store := &testStore{fail: true}
	conversation := New("thread", store, nil, nil)
	err := conversation.AddHistory(ctx, "run", schema.UserMessage("hello"))
	if err == nil {
		t.Fatal("expected persistence failure")
	}
	if len(conversation.GetHistory(ctx)) != 0 {
		t.Fatal("failed write appeared in history")
	}
}

type testCompactor struct {
	started chan struct{}
	release chan struct{}
}

func (*testCompactor) GetID() string { return "test" }
func (compactor *testCompactor) Compact(ctx context.Context, messages []*schema.Message) ([]*schema.Message, error) {
	if compactor.started != nil {
		close(compactor.started)
		select {
		case <-compactor.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	summary := schema.SystemMessage("summary")
	return append([]*schema.Message{summary}, messages[len(messages)-1:]...), nil
}
func TestContext_ReloadEqualsCompactedContext(t *testing.T) {
	ctx := context.Background()
	store := &testStore{}
	conversation := New("thread", store, &testCompactor{}, nil)
	err := conversation.AddHistory(ctx, "run", schema.UserMessage("older"), schema.UserMessage("retain"))
	if err != nil {
		t.Fatal(err)
	}
	_, compactErr := conversation.Compact(ctx, "run")
	if compactErr != nil {
		t.Fatal(compactErr)
	}
	addHistoryErr := conversation.AddHistory(ctx, "run", schema.UserMessage("later"))
	if addHistoryErr != nil {
		t.Fatal(addHistoryErr)
	}
	restored := New("thread", store, &testCompactor{}, nil)
	reloadHistoryErr := restored.ReloadHistory(ctx)
	if reloadHistoryErr != nil {
		t.Fatal(reloadHistoryErr)
	}
	messages := restored.GetHistory(ctx)
	if len(messages) != 3 || messages[0].Content != "summary" || messages[1].Content != "retain" || messages[2].Content != "later" {
		t.Fatalf("retained context lost: %v", messages)
	}
}
func TestContext_StaleCompactionCannotOverwriteNewInput(t *testing.T) {
	ctx := context.Background()
	compactor := &testCompactor{started: make(chan struct{}), release: make(chan struct{})}
	store := &testStore{}
	conversation := New("thread", store, compactor, nil)
	cAddHistoryErr := conversation.AddHistory(ctx, "run", schema.UserMessage("before"))
	if cAddHistoryErr != nil {
		t.Fatal(cAddHistoryErr)
	}
	done := make(chan error, 1)
	go func() { _, err := conversation.Compact(ctx, "run"); done <- err }()
	<-compactor.started
	addHistoryErr := conversation.AddHistory(ctx, "run", schema.UserMessage("new input"))
	if addHistoryErr != nil {
		t.Fatal(addHistoryErr)
	}
	close(compactor.release)
	checkErr := <-done
	if checkErr != nil {
		t.Fatal(checkErr)
	}
	history := conversation.GetHistory(ctx)
	if len(history) != 2 || history[0].Content != "before" || history[1].Content != "new input" {
		t.Fatalf("stale summary overwrote input: %v", history)
	}
	if len(store.records) != 2 {
		t.Fatal("stale compact was persisted")
	}
}

type DedupHistoryStore struct {
	mu      sync.Mutex
	records []*HistoryRecord
	seen    map[int64]struct{}
}

type BlockingCompaction struct {
	entered chan struct{}
	release chan struct{}
}

func (BlockingCompaction) GetID() string { return "blocking" }

func (compactor BlockingCompaction) Compact(context.Context, []*schema.Message) ([]*schema.Message, error) {
	close(compactor.entered)
	<-compactor.release
	summary := schema.SystemMessage("stale summary")
	return []*schema.Message{summary}, nil
}

func (historyStore *DedupHistoryStore) Append(_ context.Context, record *HistoryRecord) error {
	historyStore.mu.Lock()
	defer historyStore.mu.Unlock()
	if historyStore.seen == nil {
		historyStore.seen = make(map[int64]struct{})
	}
	_, exists := historyStore.seen[record.MessageID]
	if exists {
		return nil
	}
	historyStore.seen[record.MessageID] = struct{}{}
	copy := *record
	record.Seq = int64(len(historyStore.records) + 1)
	copy.Seq = record.Seq
	historyStore.records = append(historyStore.records, &copy)
	return nil
}

func (historyStore *DedupHistoryStore) LoadAfter(_ context.Context, threadID string, sequence int64, limit int) ([]*HistoryRecord, error) {
	historyStore.mu.Lock()
	defer historyStore.mu.Unlock()
	var records []*HistoryRecord
	for _, record := range historyStore.records {
		if record.ThreadID != threadID || record.Seq <= sequence {
			continue
		}
		records = append(records, record)
		if limit > 0 && len(records) >= limit {
			break
		}
	}
	return records, nil
}

func TestHistoryRedeliveryUsesDurableMessageIdentity(t *testing.T) {
	store := &DedupHistoryStore{}
	provider := func(context.Context, string, string, *schema.Message) (int64, error) { return 42, nil }
	first := New("thread-1", store, nil, nil, WithRecordID(provider))
	err := first.AddHistory(context.Background(), "run-1", schema.UserMessage("once"))
	if err != nil {
		t.Fatal(err)
	}

	second := New("thread-1", store, nil, nil, WithRecordID(provider))
	reloadHistoryErr := second.ReloadHistory(context.Background())
	if reloadHistoryErr != nil {
		t.Fatal(reloadHistoryErr)
	}
	addHistoryErr := second.AddHistory(context.Background(), "run-2", schema.UserMessage("once"))
	if addHistoryErr != nil {
		t.Fatal(addHistoryErr)
	}
	history := second.GetHistory(context.Background())
	if len(history) != 1 || history[0].Content != "once" {
		t.Fatalf("redelivery duplicated model history: %+v", history)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.records) != 1 {
		t.Fatalf("redelivery duplicated durable history: %d records", len(store.records))
	}
}

func TestStaleCompactionCannotOverwriteAcceptedHistory(t *testing.T) {
	strategy := BlockingCompaction{entered: make(chan struct{}), release: make(chan struct{})}
	conversation := New("thread-1", nil, strategy, nil)
	managerAddHistoryErr := conversation.AddHistory(context.Background(), "run-1", schema.UserMessage("old"))
	if managerAddHistoryErr != nil {
		t.Fatal(managerAddHistoryErr)
	}
	result := make(chan error, 1)
	go func() {
		_, err := conversation.Compact(context.Background(), "compact-1")
		result <- err
	}()
	<-strategy.entered
	addHistoryErr := conversation.AddHistory(context.Background(), "run-2", schema.UserMessage("fresh"))
	if addHistoryErr != nil {
		t.Fatal(addHistoryErr)
	}
	close(strategy.release)
	checkErr := <-result
	if checkErr != nil {
		t.Fatal(checkErr)
	}
	history := conversation.GetHistory(context.Background())
	if len(history) != 2 || history[0].Content != "old" || history[1].Content != "fresh" {
		t.Fatalf("stale compaction replaced accepted history: %+v", history)
	}
}

func TestReloadHistoryCrossesPageBoundary(t *testing.T) {
	store := &testStore{}
	first := New("thread", store, nil, nil)
	messages := make([]*schema.Message, 205)
	for index := range messages {
		messages[index] = schema.UserMessage(fmt.Sprintf("message %d", index))
	}
	err := first.AddHistory(context.Background(), "run", messages...)
	if err != nil {
		t.Fatal(err)
	}
	restored := New("thread", store, nil, nil)
	err = restored.ReloadHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	history := restored.GetHistory(context.Background())
	if len(history) != 205 || history[199].Content != "message 199" || history[204].Content != "message 204" || restored.SnapshotContext().HistoryCursor != 205 {
		t.Fatal("reload lost messages or sequence at the page boundary")
	}
}
