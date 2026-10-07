package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"

	dalcache "eino-cli/deepagent/dal/cache"
	daldb "eino-cli/deepagent/dal/db"
	dalmodel "eino-cli/deepagent/dal/model"
	"eino-cli/deepagent/graph/conversation"
	"github.com/cloudwego/eino/schema"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	gormschema "gorm.io/gorm/schema"
)

func TestConversationEntryHasDirectJSONMapping(t *testing.T) {
	mapping, err := gormschema.Parse(&dalmodel.ConversationEntry{}, &sync.Map{}, gormschema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	message := mapping.FieldsByName["Message"]
	if message == nil || message.DBName != "message" || message.Serializer == nil {
		t.Fatal("ConversationEntry must directly serialize the original Eino message into the message column")
	}
	compactedMessages := mapping.FieldsByName["CompactedMessages"]
	if compactedMessages == nil || compactedMessages.DBName != "ext" || compactedMessages.Serializer == nil {
		t.Fatal("ConversationEntry must directly serialize the complete compacted window into ext")
	}
}

type conversationRedis struct {
	dalcache.RedisClient
	sequences map[string]int64
}

func (redis *conversationRedis) IncrBy(_ context.Context, key string, delta int64) (int64, error) {
	if redis.sequences == nil {
		redis.sequences = make(map[string]int64)
	}
	redis.sequences[key] += delta
	return redis.sequences[key], nil
}

func TestConversationDAOMigrationAndRoundTrip(t *testing.T) {
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
	redis := &conversationRedis{sequences: map[string]int64{"deepagent:history:seq:thread:thread": 3}}
	client, err := daldb.NewSQL(context.Background(), "", "", database)
	if err != nil {
		t.Fatal(err)
	}
	store := daldb.NewConversationDAO(client, table, redis)
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
	err = store.Append(context.Background(), &dalmodel.ConversationEntry{ThreadID: "thread", RunID: "run", MessageID: 3, Type: dalmodel.ConversationEntryMessage, Message: input})
	if err != nil {
		t.Fatal(err)
	}
	conversation := conversation.New("thread", store, nil, nil)
	err = conversation.ReloadHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	history := conversation.GetHistory(context.Background())
	if len(history) != 2 || history[0].Content != "earlier goal" || history[1].Content != "next" || history[1].Extra["source"] != "round-trip" {
		t.Fatalf("reload lost migrated context or message metadata: %+v", history)
	}
	duplicate := &dalmodel.ConversationEntry{ThreadID: "thread", RunID: "other-run", MessageID: 3, Type: dalmodel.ConversationEntryMessage, Message: schema.UserMessage("must not overwrite")}
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
	foundRowsClient, err := daldb.NewSQL(context.Background(), "", "", foundRowsDatabase)
	if err != nil {
		t.Fatal(err)
	}
	foundRowsStore := daldb.NewConversationDAO(foundRowsClient, table, redis)
	duplicate.Seq = 0
	err = foundRowsStore.Append(context.Background(), duplicate)
	if err != nil || duplicate.Seq != records[0].Seq {
		t.Fatalf("clientFoundRows redelivery returned an unpersisted sequence: got=%d want=%d err=%v", duplicate.Seq, records[0].Seq, err)
	}

	rollback := errors.New("rollback history write")
	err = client.Transaction(context.Background(), func(txCtx context.Context) error {
		appendErr := store.Append(txCtx, &dalmodel.ConversationEntry{ThreadID: "rollback-thread", MessageID: 91, Type: dalmodel.ConversationEntryMessage, Message: schema.UserMessage("must roll back")})
		if appendErr != nil {
			return appendErr
		}
		pendingRecords, loadErr := store.LoadAfter(txCtx, "rollback-thread", 0, 200)
		if loadErr != nil {
			return loadErr
		}
		if len(pendingRecords) != 1 || pendingRecords[0].Message.Content != "must roll back" {
			return errors.New("history reads must see writes in the same transaction")
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	rolledBack, err := store.LoadAfter(context.Background(), "rollback-thread", 0, 200)
	if err != nil || len(rolledBack) != 0 {
		t.Fatalf("history write escaped the shared MySQL transaction: records=%d err=%v", len(rolledBack), err)
	}
}
