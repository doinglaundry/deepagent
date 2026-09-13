package distributed

import (
	"context"
	"eino-cli/deepagent/core/memory"
	"eino-cli/manager/api"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/schema"
	"log/slog"
	"time"
)

type memoryScanner struct {
	sources  api.MemorySourceStore
	userID   string
	pipeline func(string) (*memory.Pipeline, error)
	interval time.Duration
}

func (s memoryScanner) scan(ctx context.Context) error {
	var failures error
	scopes := map[string]*memory.Pipeline{}
	for offset := 0; ; offset += 100 {
		sources, err := s.sources.ListMemorySources(ctx, 100, offset)
		if err != nil {
			return errors.Join(failures, err)
		}
		for _, source := range sources {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			scope := s.userID
			if scope == "" {
				scope = source.SessionID
			}
			pipeline := scopes[scope]
			if pipeline == nil {
				pipeline, err = s.pipeline(scope)
				if err != nil {
					failures = errors.Join(failures, err)
					continue
				}
				scopes[scope] = pipeline
			}
			var messages []*schema.Message
			if err = json.Unmarshal(source.History.Messages, &messages); err != nil {
				failures = errors.Join(failures, fmt.Errorf("memory source %s: %w", source.ThreadID, err))
				continue
			}
			if err = pipeline.Observe(ctx, source.ThreadID, messages); err != nil && !errors.Is(err, api.ErrConflict) {
				failures = errors.Join(failures, fmt.Errorf("extract memory source %s: %w", source.ThreadID, err))
			}
		}
		if len(sources) < 100 {
			break
		}
	}
	for scope, pipeline := range scopes {
		if err := pipeline.Consolidate(ctx); err != nil && !errors.Is(err, api.ErrConflict) {
			failures = errors.Join(failures, fmt.Errorf("consolidate memory scope %s: %w", scope, err))
		}
	}
	return failures
}
func (s memoryScanner) start(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	if s.interval == 0 {
		s.interval = time.Minute
	}
	go func() {
		defer close(done)
		tick := time.NewTicker(s.interval)
		defer tick.Stop()
		for {
			if err := s.scan(ctx); err != nil && ctx.Err() == nil {
				slog.Error("memory source sweep", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}
