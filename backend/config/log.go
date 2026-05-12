package config

import (
	"log/slog"
	"os"
	"strings"
)

// SetLogLevel installs a stderr TextHandler at cfg.LogLevel as the slog
// default. Called once from main right after Load — before any agent boots —
// so all later slog calls (existing Warn/Info ones plus the new
// ToolCallObservability Debug ones) share the same handler/level. Unknown
// values fall back to Info so a typo in yaml doesn't silence everything.
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
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
}
