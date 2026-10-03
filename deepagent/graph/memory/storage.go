package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	memorypkg "eino-cli/deepagent/protocol/memory"

	"github.com/google/uuid"
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
				_, e := p.c.Store.RenewMemory(ctx, lease, p.c.LeaseTTL)
				if e != nil {
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

func hash(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }

func (p *memoryService) scopeRoot(scope string) string {
	return filepath.Join(p.c.Root, hash([]byte(scope)))
}

func (p *memoryService) state(scope string) (consolidated, error) {
	state := consolidated{Baselines: map[string]string{}}
	err := readJSON(filepath.Join(p.scopeRoot(scope), "memory.json"), &state)
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return state, err
}

func readJSON(path string, v any) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	e = json.Unmarshal(b, v)
	if e != nil {
		return fmt.Errorf("read %s: %w", path, e)
	}
	return nil
}

func atomicJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".memory-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, e = f.Write(b)
	if e != nil {
		f.Close()
		return e
	}
	e = f.Sync()
	if e != nil {
		f.Close()
		return e
	}
	e = f.Close()
	if e != nil {
		return e
	}
	e = os.Rename(tmp, path)
	if e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

func lock(ctx context.Context, path string) (func(), error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if e == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if !errors.Is(e, syscall.EWOULDBLOCK) && !errors.Is(e, syscall.EAGAIN) {
			f.Close()
			return nil, e
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
