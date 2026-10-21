package db

import (
	"context"
	"errors"
	"time"

	agentmodel "eino-cli/deepagent/model"

	"gorm.io/gorm"
)

// MigrateMailbox creates the Thread, Message and Run tables for the new Manager data model.
func MigrateMailbox(ctx context.Context, conn *gorm.DB) error {
	// Web 与 Worker 会独立启动；MySQL 连接级锁把同一数据库的首次迁移串行化。
	return conn.WithContext(ctx).Connection(func(connection *gorm.DB) (err error) {
		var acquired int
		err = connection.Raw("SELECT GET_LOCK(SHA2(CONCAT(DATABASE(), ':deepagent-mailbox'), 256), 10)").Scan(&acquired).Error
		if err != nil {
			return err
		}
		if acquired != 1 {
			return errors.New("timed out waiting for mailbox schema migration")
		}
		defer func() {
			releaseContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			err = errors.Join(err, connection.WithContext(releaseContext).Exec("DO RELEASE_LOCK(SHA2(CONCAT(DATABASE(), ':deepagent-mailbox'), 256))").Error)
		}()
		return connection.AutoMigrate(&agentmodel.ThreadRecord{}, &agentmodel.MailboxMessage{}, &agentmodel.RunRecord{})
	})
}

func (conversationDAO *ConversationDAO) MigrateSchema(ctx context.Context) error {
	if conversationDAO == nil || conversationDAO.Client == nil {
		return errors.New("history store is not initialized")
	}
	return conversationDAO.Client.DB(ctx, true).Table(conversationDAO.tableName).AutoMigrate(&conversationRow{})
}
