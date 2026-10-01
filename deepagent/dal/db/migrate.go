package db

import (
	"context"

	"eino-cli/deepagent/dal/model"
	"gorm.io/gorm"
)

// MigrateMailbox creates the Thread, Message and Run tables for the new Manager data model.
func MigrateMailbox(ctx context.Context, conn *gorm.DB) error {
	return conn.WithContext(ctx).AutoMigrate(&model.Thread{}, &model.Message{}, &model.RunRecord{})
}
