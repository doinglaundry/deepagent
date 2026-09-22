package manager

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"eino-cli/deepagent/manager/api"
	"eino-cli/deepagent/protocol"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type memoryJob struct {
	Key, Owner, Token string
	ExpiresAt         time.Time
	Artifact          api.MemoryArtifact
	now               time.Time
}
type memoryBackend interface {
	memoryUpdate(context.Context, string, bool, func(*memoryJob) error) (*memoryJob, error)
	memoryGet(context.Context, string) (api.MemoryArtifact, error)
	memoryList(context.Context, string, int, int) (map[string]api.MemoryArtifact, error)
}

var _ api.MemoryStore = (*Manager)(nil)
var _ api.MemoryStore = (*Memory)(nil)

func (m *engine) ClaimMemory(ctx context.Context, key, owner string, ttl time.Duration) (api.MemoryLease, error) {
	if key == "" || len(key) > 191 || owner == "" || len(owner) > 191 || ttl <= 0 {
		return api.MemoryLease{}, fmt.Errorf("memory key/owner (1-191 bytes) and positive duration required")
	}
	job, e := m.store.(memoryBackend).memoryUpdate(ctx, key, true, func(j *memoryJob) error {
		if j.Token != "" && j.ExpiresAt.After(j.now) {
			return api.ErrConflict
		}
		j.Owner = owner
		j.Token = protocol.NewID("memory-permit")
		j.ExpiresAt = j.now.Add(ttl)
		return nil
	})
	if e != nil {
		return api.MemoryLease{}, e
	}
	return jobLease(job), nil
}
func jobLease(j *memoryJob) api.MemoryLease {
	return api.MemoryLease{Key: j.Key, Token: j.Token, ExpiresAt: j.ExpiresAt}
}
func checkMemory(j *memoryJob, p api.MemoryLease) error {
	if p.Key != j.Key || p.Token == "" || p.Token != j.Token || !j.ExpiresAt.After(j.now) {
		return api.ErrPermitLost
	}
	return nil
}
func (m *engine) RenewMemory(ctx context.Context, p api.MemoryLease, ttl time.Duration) (api.MemoryLease, error) {
	if ttl <= 0 {
		return api.MemoryLease{}, fmt.Errorf("positive memory permit duration required")
	}
	j, e := m.store.(memoryBackend).memoryUpdate(ctx, p.Key, false, func(j *memoryJob) error {
		if e := checkMemory(j, p); e != nil {
			return e
		}
		j.ExpiresAt = j.now.Add(ttl)
		return nil
	})
	if e != nil {
		return api.MemoryLease{}, e
	}
	return jobLease(j), nil
}
func (m *engine) CompleteMemory(ctx context.Context, p api.MemoryLease, version string, data []byte) error {
	if version == "" {
		return fmt.Errorf("memory artifact version required")
	}
	_, e := m.store.(memoryBackend).memoryUpdate(ctx, p.Key, false, func(j *memoryJob) error {
		if e := checkMemory(j, p); e != nil {
			return e
		}
		j.Artifact = api.MemoryArtifact{Version: version, Data: append([]byte(nil), data...)}
		j.Token = ""
		j.Owner = ""
		j.ExpiresAt = time.Time{}
		return nil
	})
	return e
}
func (m *engine) ReleaseMemory(ctx context.Context, p api.MemoryLease) error {
	_, e := m.store.(memoryBackend).memoryUpdate(ctx, p.Key, false, func(j *memoryJob) error {
		if e := checkMemory(j, p); e != nil {
			return e
		}
		j.Token = ""
		j.Owner = ""
		j.ExpiresAt = time.Time{}
		return nil
	})
	return e
}
func (m *engine) GetMemory(ctx context.Context, key string) (api.MemoryArtifact, error) {
	return m.store.(memoryBackend).memoryGet(ctx, key)
}
func (m *engine) ListMemory(ctx context.Context, prefix string, limit, offset int) (map[string]api.MemoryArtifact, error) {
	return m.store.(memoryBackend).memoryList(ctx, prefix, limit, offset)
}
func copyArtifact(a api.MemoryArtifact) api.MemoryArtifact {
	a.Data = append([]byte(nil), a.Data...)
	return a
}
func (s *memoryStore) memoryUpdate(ctx context.Context, key string, create bool, fn func(*memoryJob) error) (*memoryJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(ctx); e != nil {
		return nil, e
	}
	j, ok := s.jobs[key]
	if !ok && !create {
		return nil, api.ErrNotFound
	}
	if !ok {
		j = memoryJob{Key: key}
	}
	j.Artifact = copyArtifact(j.Artifact)
	j.now = time.Now().UTC()
	if e := fn(&j); e != nil {
		return nil, e
	}
	if s.jobs == nil {
		s.jobs = map[string]memoryJob{}
	}
	stored := j
	stored.Artifact = copyArtifact(j.Artifact)
	s.jobs[key] = stored
	return &j, nil
}
func (s *memoryStore) memoryGet(ctx context.Context, key string) (api.MemoryArtifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(ctx); e != nil {
		return api.MemoryArtifact{}, e
	}
	j, ok := s.jobs[key]
	if !ok || j.Artifact.Version == "" {
		return api.MemoryArtifact{}, api.ErrNotFound
	}
	return copyArtifact(j.Artifact), nil
}
func (s *memoryStore) memoryList(ctx context.Context, prefix string, limit, offset int) (map[string]api.MemoryArtifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(ctx); e != nil {
		return nil, e
	}
	keys := []string{}
	for k, j := range s.jobs {
		if strings.HasPrefix(k, prefix) && j.Artifact.Version != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	limit, offset = bounds(limit, offset)
	out := map[string]api.MemoryArtifact{}
	for i := offset; i < len(keys) && i < offset+limit; i++ {
		out[keys[i]] = copyArtifact(s.jobs[keys[i]].Artifact)
	}
	return out, nil
}

type memoryRow struct {
	Namespace string    `gorm:"primaryKey;type:varbinary(191)"`
	Key       string    `gorm:"primaryKey;type:varbinary(191)"`
	Owner     string    `gorm:"size:191"`
	Token     string    `gorm:"size:64"`
	ExpiresAt time.Time `gorm:"precision:6"`
	Version   string    `gorm:"type:text"`
	Data      []byte    `gorm:"type:longblob"`
}

func (memoryRow) TableName() string { return "deepagent_memory_jobs" }
func (s *sqlStore) memoryUpdate(ctx context.Context, key string, create bool, fn func(*memoryJob) error) (*memoryJob, error) {
	var result *memoryJob
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if create {
			if e := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&memoryRow{Namespace: s.namespace, Key: key, ExpiresAt: time.Unix(0, 0).UTC()}).Error; e != nil {
				return e
			}
		}
		var row memoryRow
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("namespace = ? AND `key` = ?", s.namespace, key).Take(&row).Error; e != nil {
			return mapError(e)
		}
		now, e := dbNow(tx)
		if e != nil {
			return e
		}
		j := &memoryJob{Key: key, Owner: row.Owner, Token: row.Token, ExpiresAt: row.ExpiresAt, Artifact: api.MemoryArtifact{Version: row.Version, Data: row.Data}, now: now}
		if e = fn(j); e != nil {
			return e
		}
		until := j.ExpiresAt
		if until.IsZero() {
			until = time.Unix(0, 0).UTC()
		}
		if e = tx.Model(&memoryRow{}).Where("namespace = ? AND `key` = ?", s.namespace, key).Updates(map[string]any{"owner": j.Owner, "token": j.Token, "expires_at": until, "version": j.Artifact.Version, "data": j.Artifact.Data}).Error; e != nil {
			return e
		}
		result = j
		return nil
	})
	return result, e
}
func (s *sqlStore) memoryGet(ctx context.Context, key string) (api.MemoryArtifact, error) {
	var row memoryRow
	if e := s.db.WithContext(ctx).Where("namespace = ? AND `key` = ? AND version <> ''", s.namespace, key).Take(&row).Error; e != nil {
		return api.MemoryArtifact{}, mapError(e)
	}
	return api.MemoryArtifact{Version: row.Version, Data: row.Data}, nil
}
func (s *sqlStore) memoryList(ctx context.Context, prefix string, limit, offset int) (map[string]api.MemoryArtifact, error) {
	limit, offset = bounds(limit, offset)
	var rows []memoryRow
	// LEFT equality treats '%' and '_' in user keys literally instead of LIKE wildcards.
	if e := s.db.WithContext(ctx).Where("namespace = ? AND LEFT(`key`, OCTET_LENGTH(?)) = ? AND version <> ''", s.namespace, prefix, prefix).Order("`key` ASC").Limit(limit).Offset(offset).Find(&rows).Error; e != nil {
		return nil, e
	}
	out := map[string]api.MemoryArtifact{}
	for _, r := range rows {
		out[r.Key] = api.MemoryArtifact{Version: r.Version, Data: r.Data}
	}
	return out, nil
}
