package config

import (
	"io"
	"log/slog"
	"strings"
)

func SetLogLevel(cfg *Config) {
	level := slog.LevelInfo
	if cfg != nil {
		switch strings.ToLower(cfg.LogLevel) {
		case "debug":
			level = slog.LevelDebug
		case "warn":
			level = slog.LevelWarn
		case "error":
			level = slog.LevelError
		}
	}

	handler := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
}
