package managed

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type Config struct {
	Concurrency  int
	ScanInterval time.Duration
	BatchSize    int
	LeaseTTL     time.Duration
	WorkerID     string
}

func (c Config) defaults() Config {
	if c.Concurrency <= 0 {
		c.Concurrency = 8
	}
	if c.ScanInterval <= 0 {
		c.ScanInterval = time.Second
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 20
	}
	if c.LeaseTTL <= 0 {
		c.LeaseTTL = time.Minute
	}
	if c.WorkerID == "" {
		c.WorkerID = "worker"
	}
	return c
}

type Controller interface {
	Scan(context.Context, int) ([]string, error)
	Claim(context.Context, string) (any, error)
	Release(context.Context, string) error
}
type LeasedController interface {
	Controller
	ClaimLease(context.Context, string, string, time.Duration) (any, string, error)
	RenewLease(context.Context, string, string, time.Duration) error
	ReleaseLease(context.Context, string, string) error
}
type ExecuteFunc func(context.Context, any) error

type Worker struct {
	controller Controller
	execute    ExecuteFunc
	cfg        Config
}

func New(controller Controller, execute ExecuteFunc, cfg Config) *Worker {
	return &Worker{controller: controller, execute: execute, cfg: cfg.defaults()}
}
func (w *Worker) Run(ctx context.Context) error {
	if w == nil || w.controller == nil || w.execute == nil {
		return nil
	}
	sem := make(chan struct{}, w.cfg.Concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()
	ticker := time.NewTicker(w.cfg.ScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			ids, err := w.controller.Scan(ctx, w.cfg.BatchSize)
			if err != nil {
				slog.ErrorContext(ctx, "worker scan failed", "error", err)
				continue
			}
			for _, id := range ids {
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return ctx.Err()
				}
				var item any
				var token string
				var err error
				if lc, ok := w.controller.(LeasedController); ok {
					item, token, err = lc.ClaimLease(ctx, id, w.cfg.WorkerID, w.cfg.LeaseTTL)
				} else {
					item, err = w.controller.Claim(ctx, id)
				}
				if err != nil {
					<-sem
					slog.ErrorContext(ctx, "worker claim failed", "thread_id", id, "error", err)
					continue
				}
				wg.Add(1)
				go func(id string, item any, token string) {
					defer wg.Done()
					defer func() { <-sem }()
					leaseCtx, cancel := context.WithCancel(ctx)
					defer cancel()
					var leaseWG sync.WaitGroup
					if token != "" {
						leaseWG.Add(1)
						go func() {
							defer leaseWG.Done()
							ticker := time.NewTicker(w.cfg.LeaseTTL / 3)
							defer ticker.Stop()
							for {
								select {
								case <-ticker.C:
									if lc, ok := w.controller.(LeasedController); ok {
										if err := lc.RenewLease(leaseCtx, id, token, w.cfg.LeaseTTL); err != nil {
											cancel()
											return
										}
									}
								case <-leaseCtx.Done():
									return
								}
							}
						}()
					}
					if err := w.execute(leaseCtx, item); err != nil {
						slog.ErrorContext(ctx, "worker execution failed", "thread_id", id, "error", err)
					}
					cancel()
					leaseWG.Wait()
					var releaseErr error
					if token != "" {
						releaseErr = w.controller.(LeasedController).ReleaseLease(context.WithoutCancel(ctx), id, token)
					} else {
						releaseErr = w.controller.Release(context.WithoutCancel(ctx), id)
					}
					if err := releaseErr; err != nil {
						slog.ErrorContext(ctx, "worker release failed", "thread_id", id, "error", err)
					}
				}(id, item, token)
			}
		}
	}
}
