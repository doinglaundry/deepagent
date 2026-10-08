package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"

	dalcache "eino-cli/deepagent/dal/cache"
	daldb "eino-cli/deepagent/dal/db"
	"eino-cli/deepagent/graph/conversation"
	messagepkg "eino-cli/deepagent/message"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

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

func TestConversationDAORoundTrip(t *testing.T) {
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
		t.Skip("set DEEPAGENT_TEST_MYSQL_DSN or DEEPAGENT_TEST_CONFIG for real conversation storage validation")
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
	table := "conversation_message_" + uuid.NewString()[:8]
	client, err := daldb.NewSQL(context.Background(), "", "", database)
	if err != nil {
		t.Fatal(err)
	}
	store := daldb.NewConversationDAO(client, table, &conversationRedis{})
	ctx := context.Background()
	err = store.MigrateSchema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Migrator().DropTable(table) })
	err = store.MigrateSchema(ctx)
	if err != nil {
		t.Fatal("schema setup must be idempotent:", err)
	}
	first := &messagepkg.Message{ThreadID: "thread", RunID: "run", MessageID: "9007199254740993", SenderID: "person", SenderType: "user", Role: schema.User, Content: "old", CreatedAt: 42, Extra: map[string]any{"source": "round-trip"}}
	retained := &messagepkg.Message{ThreadID: "thread", RunID: "run", MessageID: "2", Role: schema.User, Content: "retain"}
	for _, message := range []*messagepkg.Message{first, retained} {
		err = store.AppendMessage(ctx, message)
		if err != nil {
			t.Fatal(err)
		}
	}
	summary := &messagepkg.Message{ThreadID: "thread", RunID: "run", MessageID: "3", Role: schema.System, Content: "summary"}
	err = store.SaveContext(ctx, summary, []*messagepkg.Message{summary, retained})
	if err != nil {
		t.Fatal(err)
	}
	next := &messagepkg.Message{ThreadID: "thread", RunID: "next-run", MessageID: "4", Role: schema.User, Content: "next"}
	err = store.AppendMessage(ctx, next)
	if err != nil {
		t.Fatal(err)
	}
	loaded, ids, cursor, err := store.LoadContext(ctx, "thread")
	if err != nil || len(loaded) != 3 || loaded[0].Content != "summary" || loaded[1].Content != "retain" || loaded[2].Content != "next" || len(ids) != 4 || cursor != 4 {
		t.Fatalf("reload lost context or identities: messages=%+v ids=%v cursor=%d err=%v", loaded, ids, cursor, err)
	}
	if loaded[0].MessageID != "3" || loaded[0].Seq != 3 || loaded[2].RunID != "next-run" {
		t.Fatalf("reload lost message metadata: %+v", loaded)
	}
	copy := *first
	copy.Content = "must not overwrite"
	copy.Seq = 0
	err = store.AppendMessage(ctx, &copy)
	if err != nil || copy.Seq != first.Seq {
		t.Fatalf("redelivery changed durable sequence: got=%d want=%d err=%v", copy.Seq, first.Seq, err)
	}
	restored := conversation.New("thread", store, nil, nil)
	err = restored.ReloadHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = restored.AddHistory(ctx, "redelivery", &copy)
	if err != nil || len(restored.GetHistory(ctx)) != 3 {
		t.Fatal("compacted message was redelivered", err)
	}
	rollback := errors.New("rollback conversation write")
	err = client.Transaction(ctx, func(txCtx context.Context) error {
		writeErr := store.AppendMessage(txCtx, &messagepkg.Message{ThreadID: "rollback", MessageID: "9", Role: schema.User, Content: "in transaction"})
		if writeErr != nil {
			return writeErr
		}
		pending, _, _, loadErr := store.LoadContext(txCtx, "rollback")
		if loadErr != nil {
			return loadErr
		}
		if len(pending) != 1 || pending[0].Content != "in transaction" {
			return errors.New("conversation must see writes in the same transaction")
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	rolledBack, _, _, err := store.LoadContext(ctx, "rollback")
	if err != nil || len(rolledBack) != 0 {
		t.Fatalf("write escaped rollback: messages=%v err=%v", rolledBack, err)
	}
	for index := 0; index < 205; index++ {
		message := &messagepkg.Message{ThreadID: "pages", MessageID: fmt.Sprint(index + 1), Role: schema.User, Content: fmt.Sprint(index)}
		err = store.AppendMessage(ctx, message)
		if err != nil {
			t.Fatal(err)
		}
	}
	pages, _, sequence, err := store.LoadContext(ctx, "pages")
	if err != nil || len(pages) != 205 || pages[200].Content != "200" || sequence != 205 {
		t.Fatalf("page boundary lost messages: count=%d cursor=%d err=%v", len(pages), sequence, err)
	}
	// Check full metadata before any compaction replaces this thread's window.
	err = store.AppendMessage(ctx, &messagepkg.Message{ThreadID: "metadata", MessageID: first.MessageID, RunID: first.RunID, CreatedAt: first.CreatedAt, Role: first.Role, SenderID: first.SenderID, SenderType: first.SenderType, Content: first.Content, Extra: first.Extra})
	if err != nil {
		t.Fatal(err)
	}
	metadata, _, _, err := store.LoadContext(ctx, "metadata")
	if err != nil || len(metadata) != 1 || metadata[0].SenderID != "person" || metadata[0].CreatedAt != 42 || metadata[0].Extra["source"] != "round-trip" {
		t.Fatalf("metadata lost: messages=%v err=%v", metadata, err)
	}
	raw, err := json.Marshal(metadata[0])
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip messagepkg.Message
	err = json.Unmarshal(raw, &roundTrip)
	if err != nil || roundTrip.MessageID != "9007199254740993" {
		t.Fatal("large message identity was truncated", err)
	}
}
