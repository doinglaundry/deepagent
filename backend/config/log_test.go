package config

import (
	"context"
	"log/slog"
	"testing"
)

func TestSetLogLevelReadsConfig(t *testing.T) {
	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })

	SetLogLevel(&Config{LogLevel: "error"})
	if slog.Default().Enabled(context.Background(), slog.LevelWarn) {
		t.Fatal("warn should be disabled at error level")
	}
	if !slog.Default().Enabled(context.Background(), slog.LevelError) {
		t.Fatal("error should be enabled at error level")
	}
}
