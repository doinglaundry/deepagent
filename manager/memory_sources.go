package manager

import (
	"context"
	"eino-cli/manager/api"
	"encoding/json"
	"sort"
)

type memorySourceBackend interface {
	memorySources(context.Context, int, int) ([]api.MemorySource, error)
}

var _ api.MemorySourceStore = (*Manager)(nil)
var _ api.MemorySourceStore = (*Memory)(nil)

func (m *engine) ListMemorySources(ctx context.Context, limit, offset int) ([]api.MemorySource, error) {
	return m.store.(memorySourceBackend).memorySources(ctx, limit, offset)
}
func nonemptyHistory(h api.History) bool {
	var messages []json.RawMessage
	return h.Version > 0 && json.Unmarshal(h.Messages, &messages) == nil && len(messages) > 0
}
func memorySource(r *record) api.MemorySource {
	return api.MemorySource{ThreadID: r.Thread.ID, SessionID: r.Thread.SessionID, History: r.History, UpdatedAt: r.Thread.UpdatedAt}
}
func (s *memoryStore) memorySources(ctx context.Context, limit, offset int) ([]api.MemorySource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(ctx); e != nil {
		return nil, e
	}
	records := []*record{}
	for _, r := range s.threads {
		if (r.Thread.State == api.Idle || r.Thread.State == api.Blocked || r.Thread.State == api.Closed) && nonemptyHistory(r.History) {
			records = append(records, r)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Thread.CreatedAt.Equal(records[j].Thread.CreatedAt) {
			return records[i].Thread.ID < records[j].Thread.ID
		}
		return records[i].Thread.CreatedAt.Before(records[j].Thread.CreatedAt)
	})
	limit, offset = bounds(limit, offset)
	out := []api.MemorySource{}
	for i := offset; i < len(records) && i < offset+limit; i++ {
		out = append(out, memorySource(cloneRecord(records[i])))
	}
	return out, nil
}
func (s *sqlStore) memorySources(ctx context.Context, limit, offset int) ([]api.MemorySource, error) {
	limit, offset = bounds(limit, offset)
	var rows []threadRow
	if e := s.db.WithContext(ctx).Where("namespace = ? AND has_history = ? AND state IN ?", s.namespace, true, []string{string(api.Idle), string(api.Blocked), string(api.Closed)}).Order("created_at ASC, id ASC").Limit(limit).Offset(offset).Find(&rows).Error; e != nil {
		return nil, e
	}
	out := make([]api.MemorySource, 0, len(rows))
	for _, row := range rows {
		r, e := decode(row)
		if e != nil {
			return nil, e
		}
		if e = s.loadModelHistory(s.db.WithContext(ctx), r); e != nil {
			return nil, e
		}
		out = append(out, memorySource(r))
	}
	return out, nil
}
