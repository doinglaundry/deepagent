// Package memory implements optional two-stage extraction and consolidation.
// Root must be a shared filesystem supporting flock and atomic rename across workers.
package memory

import (
	"context"
	"crypto/sha256"
	memorypkg "eino-cli/deepagent/protocol/memory"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type Config struct {
	Store        memorypkg.Store
	LeaseTTL     time.Duration
	Root         string
	Model        model.ToolCallingChatModel
	Consolidator func(context.Context, string, string) (string, error)
}
type Service interface {
	Read(ctx context.Context, scope string) (*Snapshot, error)
	Observe(ctx context.Context, scope, threadID string, messages []*schema.Message) error
	Consolidate(ctx context.Context, scope string) error
}
type Snapshot struct {
	Scope     string
	Summary   string
	UpdatedAt time.Time
}
type memoryService struct{ c Config }
type extraction struct {
	Source, Version, Raw string
	UpdatedAt            time.Time
}
type consolidated struct {
	Summary   string
	Baselines map[string]string
	UpdatedAt time.Time
}

func New(c Config) (Service, error) {
	if c.Model == nil || c.Root == "" {
		return nil, errors.New("memory requires model and root")
	}
	if c.LeaseTTL <= 0 {
		c.LeaseTTL = 30 * time.Second
	}
	if c.LeaseTTL < 3*time.Millisecond {
		return nil, errors.New("memory lease TTL must be at least 3ms")
	}
	root, e := filepath.Abs(c.Root)
	if e != nil {
		return nil, e
	}
	c.Root = root
	if e = os.MkdirAll(root, 0700); e != nil {
		return nil, e
	}
	if c.Consolidator == nil {
		c.Consolidator = AgentConsolidator(c.Model, c.Root)
	}
	return &memoryService{c}, nil
}
func hash(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }
func (p *memoryService) Observe(ctx context.Context, scope, source string, messages []*schema.Message) error {
	if err := validateScope(ctx, scope); err != nil {
		return err
	}
	if p.c.Store != nil {
		return p.observeShared(ctx, scope, source, messages)
	}
	if source == "" {
		return errors.New("memory source required")
	}
	payload, e := json.Marshal(messages)
	if e != nil {
		return e
	}
	root := p.scopeRoot(scope)
	if err := os.MkdirAll(filepath.Join(root, "sources"), 0700); err != nil {
		return err
	}
	version := hash(payload)
	name := hash([]byte(source))
	unlock, e := lock(ctx, filepath.Join(root, "sources", name+".lock"))
	if e != nil {
		return e
	}
	defer unlock()
	path := filepath.Join(root, "sources", name+".json")
	var previous extraction
	if e = readJSON(path, &previous); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if previous.Version == version {
		return nil
	}
	// Persist source baseline and extraction together only after successful generation.
	text, e := p.extract(ctx, payload)
	if e != nil {
		return e
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	return atomicJSON(path, extraction{Source: source, Version: version, Raw: text, UpdatedAt: time.Now().UTC()})
}
func (p *memoryService) Consolidate(ctx context.Context, scope string) error {
	if err := validateScope(ctx, scope); err != nil {
		return err
	}
	if p.c.Store != nil {
		return p.consolidateShared(ctx, scope)
	}
	root := p.scopeRoot(scope)
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	unlock, e := lock(ctx, filepath.Join(root, "consolidate.lock"))
	if e != nil {
		return e
	}
	defer unlock()
	state, e := p.state(scope)
	if e != nil {
		return e
	}
	files, e := filepath.Glob(filepath.Join(root, "sources", "*.json"))
	if e != nil {
		return e
	}
	var changed []extraction
	for _, f := range files {
		var ex extraction
		if e = readJSON(f, &ex); e != nil {
			return e
		}
		if state.Baselines[ex.Source] != ex.Version {
			changed = append(changed, ex)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	raw, e := json.Marshal(changed)
	if e != nil {
		return e
	}
	text, e := p.c.Consolidator(ctx, state.Summary, string(raw))
	if e != nil {
		return e
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("empty consolidated memory")
	}
	state.Summary = text
	state.UpdatedAt = time.Now().UTC()
	for _, ex := range changed {
		state.Baselines[ex.Source] = ex.Version
	}
	return atomicJSON(filepath.Join(root, "memory.json"), state)
}
func (p *memoryService) Read(ctx context.Context, scope string) (*Snapshot, error) {
	if err := validateScope(ctx, scope); err != nil {
		return nil, err
	}
	var state consolidated
	var err error
	if p.c.Store != nil {
		state, err = p.sharedState(ctx, scope)
	} else {
		state, err = p.state(scope)
	}
	if err != nil {
		return nil, err
	}
	return &Snapshot{Scope: scope, Summary: state.Summary, UpdatedAt: state.UpdatedAt}, nil
}
func (p *memoryService) scopeRoot(scope string) string {
	return filepath.Join(p.c.Root, hash([]byte(scope)))
}
func validateScope(ctx context.Context, scope string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if scope == "" || strings.TrimSpace(scope) != scope || strings.HasSuffix(scope, "/") || strings.ContainsRune(scope, 0) {
		return errors.New("memory requires a nonempty canonical scope")
	}
	return nil
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
	if e = json.Unmarshal(b, v); e != nil {
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
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(tmp, path); e != nil {
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
