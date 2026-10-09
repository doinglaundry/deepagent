package manager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"eino-cli/deepagent/dal/cache"
	"eino-cli/deepagent/dal/db"
	agentmodel "eino-cli/deepagent/model"
)

type Manager struct {
	threads                 *db.ThreadDAO
	messages                *db.MessageDAO
	runs                    *db.RunDAO
	redis                   agentmodel.RedisClient
	db                      *db.MySQLClient
	stream                  *StreamStreamOut
	subscribeSessionMaxIdle time.Duration
}

type Config struct {
	MySQLDSN      string `yaml:"mysql_dsn"`
	MySQLReadDSN  string `yaml:"mysql_read_dsn"`
	RedisAddr     string `yaml:"redis_addr"`
	RedisPassword string `yaml:"redis_password"`
	RedisDB       int    `yaml:"redis_db"`
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.MySQLDSN) == "" || strings.TrimSpace(c.RedisAddr) == "" {
		return errors.New("manager mysql_dsn and redis_addr are required")
	}
	return nil
}

// Open owns the shared Manager storage connections and mailbox migration.
func Open(ctx context.Context, cfg Config) (*Manager, error) {
	err := cfg.Validate()
	if err != nil {
		return nil, err
	}
	client, err := db.NewSQL(ctx, cfg.MySQLDSN, cfg.MySQLReadDSN)
	if err != nil {
		return nil, fmt.Errorf("connect MySQL: %w", err)
	}
	err = db.MigrateMailbox(ctx, client.DB(ctx, true))
	if err != nil {
		return nil, fmt.Errorf("migrate mailbox: %w", err)
	}
	redisClient, err := cache.NewRedis(cache.RedisConfig{
		Addr: cfg.RedisAddr, Password: cfg.RedisPassword, DB: cfg.RedisDB,
	})
	if err != nil {
		return nil, fmt.Errorf("connect Redis: %w", err)
	}
	return New(client, redisClient)
}

func New(client *db.MySQLClient, redisClient agentmodel.RedisClient) (*Manager, error) {
	if client == nil || redisClient == nil {
		return nil, errors.New("MySQL and Redis are required")
	}
	return &Manager{
		threads:                 &db.ThreadDAO{Client: client},
		messages:                &db.MessageDAO{Client: client},
		runs:                    &db.RunDAO{Client: client},
		redis:                   redisClient,
		db:                      client,
		stream:                  &StreamStreamOut{redis: redisClient},
		subscribeSessionMaxIdle: 30 * time.Minute,
	}, nil
}

func (c *Manager) DB() *db.MySQLClient { return c.db }

func (c *Manager) Redis() agentmodel.RedisClient { return c.redis }

var (
	ErrThreadNotFound       = errors.New("thread not found")
	ErrThreadClosed         = errors.New("thread is closing or closed")
	ErrThreadNotRunnable    = errors.New("thread not runnable")
	ErrThreadNotBlocked     = errors.New("thread is not blocked")
	ErrThreadBlocked        = errors.New("thread is blocked")
	ErrLeaseMismatch        = errors.New("lease mismatch")
	ErrInvalidCancel        = errors.New("invalid cancel")
	ErrInvalidClose         = errors.New("invalid close")
	ErrMessageNotFound      = errors.New("message not found")
	InputErrMessageNotFound = errors.New("input message not found")
	ErrOutputUnavailable    = errors.New("output unavailable")
	ErrRunIDRequired        = errors.New("run ID required")
)
