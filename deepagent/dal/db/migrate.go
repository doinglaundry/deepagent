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
	return conversationDAO.Client.DB(ctx, true).Table(conversationDAO.tableName).AutoMigrate(&conversationRow{})
}
