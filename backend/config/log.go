package config

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

func SetLogLevel(cfg *Config) {
	level := slog.LevelInfo
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	var w = io.Discard
	if path := defaultLogPath(cfg); path != "" {
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			w = f
		}
	}
	handler := slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
}

func defaultLogPath(cfg *Config) string {
	if cfg == nil || strings.TrimSpace(cfg.RootDir) == "" {
		return ""
	}
	if err := os.MkdirAll(BaseDir(cfg), 0o755); err != nil {
		return ""
	}
	return LogPath(cfg)
}
