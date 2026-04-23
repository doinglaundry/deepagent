package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"eino-cli/internal/config/schema"
)

type Config = schema.Config

func Load(root string) (Config, error) {
	if root == "" {
		var err error
		root, err = os.Getwd()
		if err != nil {
			return Config{}, fmt.Errorf("get working directory: %w", err)
		}
	}

	stateDir := filepath.Join(root, ".eino-cli")
	cfg := Config{
		RootDir:        root,
		StateDir:       stateDir,
		SessionsDir:    filepath.Join(stateDir, "sessions"),
		MemoryDir:      filepath.Join(stateDir, "memory"),
		CheckpointDir:  filepath.Join(stateDir, "checkpoints"),
		RuntimeBaseURL: envOrDefault("EINO_RUNTIME_BASE_URL", "http://127.0.0.1:8080"),
		RuntimeModel:   envOrDefault("EINO_RUNTIME_MODEL", "local-model"),
		RuntimeTimeout: envOrDefaultInt("EINO_RUNTIME_TIMEOUT", 10),
	}

	if err := ensureDirs(cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func Default() Config {
	cfg, err := Load("")
	if err != nil {
		return Config{}
	}

	return cfg
}

func ensureDirs(cfg Config) error {
	for _, dir := range []string{cfg.StateDir, cfg.SessionsDir, cfg.MemoryDir, cfg.CheckpointDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create state directory %s: %w", dir, err)
		}
	}

	return nil
}

func envOrDefault(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func envOrDefaultInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
