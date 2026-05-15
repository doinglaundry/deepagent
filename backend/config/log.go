package config

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// SetLogLevel installs a TextHandler at cfg.LogLevel as the slog default.
// Called once from main right after Load — before any agent boots — so all
// later slog calls (existing Warn/Info ones plus ToolCallObservability Debug
// ones) share the same handler/level. Unknown values fall back to Info so a
// typo in yaml doesn't silence everything.
//
// The sink is <RootDir>/.eino-cli/eino-cli.log, NOT os.Stderr: bubbletea
// owns the terminal in alt-screen mode and any goroutine slog write to
// stderr would punch through the rendered chrome (the input box drift seen
// when memory_update / summarisation Warns fire mid-stream). The log file
// sits next to checkpoints / history.txt / sessions, lifecycle identical.
//
// File handle is intentionally leaked — TextHandler is unbuffered (each
// Write hits disk synchronously) and the process exit closes it for free.
// On open failure (perm denied, disk full) the handler falls back to
// io.Discard rather than stderr, because corrupted-TUI is worse than
// silent-logs.
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

	var w io.Writer = io.Discard
	if path := defaultLogPath(cfg); path != "" {
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			w = f
		}
	}
	handler := slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
}

// defaultLogPath returns <RootDir>/.eino-cli/eino-cli.log, or "" when cfg
// or RootDir is missing / the parent dir can't be created. Empty result
// asks SetLogLevel to use io.Discard.
func defaultLogPath(cfg *Config) string {
	if cfg == nil || strings.TrimSpace(cfg.RootDir) == "" {
		return ""
	}
	dir := filepath.Join(cfg.RootDir, ".eino-cli")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	return filepath.Join(dir, "eino-cli.log")
}
