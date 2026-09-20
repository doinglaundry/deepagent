package manager

import (
	"context"
	"errors"
	"time"

	"eino-cli/deepagent/dal/cache"
	"eino-cli/deepagent/dal/db"
	"gorm.io/gorm"
)

func New(sql *gorm.DB, redisClient cache.RedisClient) (*Manager, error) {
	if sql == nil || redisClient == nil {
		return nil, errors.New("MySQL and Redis are required")
	}
	client, err := db.NewSQL(context.Background(), "", "", sql)
	if err != nil {
		return nil, err
	}
	return &Manager{
		threads:                 &db.ThreadDAO{Client: client},
		messages:                &db.MessageDAO{Client: client},
		redis:                   redisClient,
		db:                      client,
		stream:                  &StreamStreamOut{redis: redisClient},
		subscribeSessionMaxIdle: 30 * time.Minute,
	}, nil
}
