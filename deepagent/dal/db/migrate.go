package db

import (
	"context"
	"errors"

	"eino-cli/deepagent/dal/model"
	"gorm.io/gorm"
)

// MigrateMailbox creates the Thread, Message and Run tables for the new Manager data model.
func MigrateMailbox(ctx context.Context, conn *gorm.DB) error {
	return conn.WithContext(ctx).AutoMigrate(&model.Thread{}, &model.Message{}, &model.RunRecord{})
}

func (conversationDAO *ConversationDAO) MigrateSchema(ctx context.Context) error {
	if conversationDAO == nil || conversationDAO.Client == nil {
		return errors.New("history store is not initialized")
	}
	database := conversationDAO.Client.DB(ctx, true).Table(conversationDAO.tableName)
	err := database.AutoMigrate(&model.ConversationEntry{})
	if err != nil {
		return err
	}
	// Convert existing compact records once. Normal reads only decode message lists.
	payload := "JSON_UNQUOTE(JSON_EXTRACT(ext, '$.CompactStrategyPayload'))"
	summary := "JSON_EXTRACT(" + payload + ", '$.Summary')"
	retained := "JSON_EXTRACT(" + payload + ", '$.Retained')"
	window := "JSON_MERGE_PRESERVE(JSON_ARRAY(" + summary + "), IF(JSON_TYPE(" + retained + ") = 'ARRAY', " + retained + ", JSON_ARRAY()))"
	return database.Where("type = ? AND JSON_VALID(ext) AND JSON_TYPE(ext) = 'OBJECT'", model.ConversationEntryCompact).
		Where("JSON_UNQUOTE(JSON_EXTRACT(ext, '$.CompactStrategyID')) = ?", "core_snapshot_v1").
		Where("JSON_EXTRACT("+payload+", '$.Version') = 1 AND JSON_TYPE("+summary+") = 'OBJECT'").
		UpdateColumn("ext", gorm.Expr(window)).Error
}
