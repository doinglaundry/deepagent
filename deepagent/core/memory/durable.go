package memory

import (
	"context"
	memorypkg "eino-cli/deepagent/protocol/memory"
	"encoding/json"
	"errors"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"strings"
	"time"
)

func (p *memoryService) key(scope, suffix string) string {
	return scope + "/" + suffix
}

// Independent renewable leases protect long-term jobs; a lost lease cancels model work
// and CompleteMemory fences every artifact/baseline mutation against the live token.
func (p *memoryService) job(ctx context.Context, key string, work func(context.Context, memorypkg.Lease) error) error {
	lease, e := p.c.Store.ClaimMemory(ctx, key, uuid.NewString(), p.c.LeaseTTL)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(p.c.LeaseTTL / 3)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, e := p.c.Store.RenewMemory(ctx, lease, p.c.LeaseTTL); e != nil {
					cancel(e)
					return
				}
			}
		}
	}()
	defer func() {
		close(done)
		cancel(nil)
		<-stopped
		releaseCtx, cc := context.WithTimeout(context.Background(), 5*time.Second)
		defer cc()
		_ = p.c.Store.ReleaseMemory(releaseCtx, lease)
	}()
	err := work(ctx, lease)
	return errors.Join(err, context.Cause(ctx))
}
func (p *memoryService) observeShared(ctx context.Context, scope, source string, messages []*schema.Message) error {
	if source == "" {
		return errors.New("memory source required")
	}
	raw, e := json.Marshal(messages)
	if e != nil {
		return e
	}
	version := hash(raw)
	key := p.key(scope, "source/"+hash([]byte(source)))
	return p.job(ctx, key, func(ctx context.Context, lease memorypkg.Lease) error {
		previous, e := p.c.Store.GetMemory(ctx, key)
		if e != nil && !errors.Is(e, memorypkg.ErrNotFound) {
			return e
		}
		if previous.Version == version {
			return nil
		}
		text, e := p.extract(ctx, raw)
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		artifact, e := json.Marshal(extraction{Source: source, Version: version, Raw: text, UpdatedAt: time.Now().UTC()})
		if e != nil {
			return e
		}
		return p.c.Store.CompleteMemory(ctx, lease, version, artifact)
	})
}
func (p *memoryService) sharedState(ctx context.Context, scope string) (consolidated, error) {
	s := consolidated{Baselines: map[string]string{}}
	a, e := p.c.Store.GetMemory(ctx, p.key(scope, "consolidation"))
	if errors.Is(e, memorypkg.ErrNotFound) {
		return s, nil
	}
	if e != nil {
		return s, e
	}
	if len(a.Data) > 0 {
		e = json.Unmarshal(a.Data, &s)
	}
	return s, e
}
func (p *memoryService) consolidateShared(ctx context.Context, scope string) error {
	return p.job(ctx, p.key(scope, "consolidation"), func(ctx context.Context, lease memorypkg.Lease) error {
		state, e := p.sharedState(ctx, scope)
		if e != nil {
			return e
		}
		var changed []extraction
		for offset := 0; ; offset += 100 {
			artifacts, e := p.c.Store.ListMemory(ctx, p.key(scope, "source/"), 100, offset)
			if e != nil {
				return e
			}
			for _, a := range artifacts {
				if len(a.Data) == 0 {
					continue
				}
				var ex extraction
				if e = json.Unmarshal(a.Data, &ex); e != nil {
					return e
				}
				if state.Baselines[ex.Source] != ex.Version {
					changed = append(changed, ex)
				}
			}
			if len(artifacts) < 100 {
				break
			}
		}
		if len(changed) == 0 {
			return nil
		}
		raw, e := json.Marshal(changed)
		if e != nil {
			return e
		}
		summary, e := p.c.Consolidator(ctx, state.Summary, string(raw))
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if strings.TrimSpace(summary) == "" {
			return errors.New("empty consolidated memory")
		}
		state.Summary = summary
		state.UpdatedAt = time.Now().UTC()
		for _, ex := range changed {
			state.Baselines[ex.Source] = ex.Version
		}
		artifact, e := json.Marshal(state)
		if e != nil {
			return e
		}
		return p.c.Store.CompleteMemory(ctx, lease, hash(artifact), artifact)
	})
}
