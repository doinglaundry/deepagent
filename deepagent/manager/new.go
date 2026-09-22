package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"eino-cli/deepagent/dal/cache"
	"eino-cli/deepagent/dal/db"
	"gopkg.in/yaml.v3"
)

type Config struct {
	MySQLDSN      string `yaml:"mysql_dsn"`
	MySQLReadDSN  string `yaml:"mysql_read_dsn"`
	RedisAddr     string `yaml:"redis_addr"`
	RedisPassword string `yaml:"redis_password"`
	RedisDB       int    `yaml:"redis_db"`
}

func LoadConfig(path string) (Config, error) {
	var document struct {
		Manager Config `yaml:"manager"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read manager config: %w", err)
	}
	if err = yaml.Unmarshal([]byte(os.ExpandEnv(string(data))), &document); err != nil {
		return Config{}, fmt.Errorf("parse manager config: %w", err)
	}
	if strings.TrimSpace(document.Manager.MySQLDSN) == "" || strings.TrimSpace(document.Manager.RedisAddr) == "" {
		return Config{}, errors.New("manager mysql_dsn and redis_addr are required")
	}
	return document.Manager, nil
}

// Open owns the shared Manager storage connections and mailbox migration.
func Open(ctx context.Context, cfg Config) (*Manager, error) {
	client, err := db.NewSQL(ctx, cfg.MySQLDSN, cfg.MySQLReadDSN)
	if err != nil {
		return nil, fmt.Errorf("connect MySQL: %w", err)
	}
	if err = db.MigrateMailbox(ctx, client.DB(ctx, true)); err != nil {
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

func New(client *db.MySQLClient, redisClient cache.RedisClient) (*Manager, error) {
	if client == nil || redisClient == nil {
		return nil, errors.New("MySQL and Redis are required")
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
