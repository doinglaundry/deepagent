package distributed

import (
	"context"
	"eino-cli/deepagent/core/compact"
	"eino-cli/deepagent/manager/api"
	"encoding/json"
	"github.com/cloudwego/eino/schema"
	"sync"
	"time"
)

// History uses the Manager's version check and permit fencing for every save.
type History struct {
	manager api.Manager
	permit  api.Permit
	base    context.Context
	mu      sync.Mutex
	history api.History
}

func NewHistory(ctx context.Context, m api.Manager, p api.Permit) *History {
	return &History{manager: m, permit: p, base: ctx}
}
func (h *History) Load(ctx context.Context) ([]*schema.Message, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	// Core construction has no context parameter. Bind its startup read to the claim.
	callCtx, cancel := context.WithTimeout(h.base, 10*time.Second)
	defer cancel()
	stored, err := h.manager.LoadHistory(callCtx, h.permit.ThreadID)
	if err != nil {
		return nil, err
	}
	h.history = stored
	if len(stored.Messages) == 0 {
		return nil, nil
	}
	var messages []*schema.Message
	err = json.Unmarshal(stored.Messages, &messages)
	return messages, err
}
func (h *History) Save(ctx context.Context, messages []*schema.Message) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	data, err := json.Marshal(messages)
	if err != nil {
		return err
	}
	next := h.history
	next.Messages = data
	next, err = h.manager.SaveHistory(ctx, h.permit, next)
	if err == nil {
		h.history = next
	}
	return err
}

type Checkpoints struct {
	Manager api.Manager
	Permit  api.Permit
}

func (c Checkpoints) Get(ctx context.Context, key string) ([]byte, bool, error) {
	data, err := c.Manager.GetCheckpoint(ctx, key)
	return data, err == nil && data != nil, err
}
func (c Checkpoints) Set(ctx context.Context, key string, data []byte) error {
	if c.Permit.Token != "" {
		if fenced, ok := c.Manager.(interface {
			PutThreadCheckpoint(context.Context, api.Permit, string, []byte) error
		}); ok {
			return fenced.PutThreadCheckpoint(ctx, c.Permit, key, data)
		}
	}
	return c.Manager.PutCheckpoint(ctx, key, data)
}

// SaveCompacted atomically advances the effective history and its compaction log.
func (h *History) SaveCompacted(ctx context.Context, messages []*schema.Message, record compact.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	data, err := json.Marshal(messages)
	if err != nil {
		return err
	}
	var records []compact.Record
	if len(h.history.Compactions) > 0 {
		if err = json.Unmarshal(h.history.Compactions, &records); err != nil {
			return err
		}
	}
	records = append(records, record)
	encoded, err := json.Marshal(records)
	if err != nil {
		return err
	}
	next := h.history
	next.Messages = data
	next.Compactions = encoded
	next, err = h.manager.SaveHistory(ctx, h.permit, next)
	if err == nil {
		h.history = next
	}
	return err
}
