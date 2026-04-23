package config

import (
	"fmt"
	"os"
	"path/filepath"

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
		RootDir:       root,
		StateDir:      stateDir,
		SessionsDir:   filepath.Join(stateDir, "sessions"),
		MemoryDir:     filepath.Join(stateDir, "memory"),
		CheckpointDir: filepath.Join(stateDir, "checkpoints"),
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
