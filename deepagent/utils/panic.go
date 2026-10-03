package utils

import (
	"context"
	"log/slog"
)

// PanicGuard converts a panic in an asynchronous graph callback into a log
// entry so the stream cleanup deferred by the caller can still run.
func PanicGuard(ctx context.Context) {
	recovered := recover()
	if recovered != nil {
		slog.ErrorContext(ctx, "panic in graph callback", "panic", recovered)
	}
}
