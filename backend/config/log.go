package config

import (
	"io"
	"log/slog"
	"os"
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

	var w = io.Discard
	if path := defaultLogPath(); path != "" {
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			w = f
		}
	}
	handler := slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
}

func defaultLogPath() string {
	if err := os.MkdirAll(BaseDir(), 0o755); err != nil {
		return ""
	}
	return LogPath()
}
