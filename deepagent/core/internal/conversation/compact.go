package conversation

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/schema"
)

type CompactSnapshot struct {
	Version       int
	SourceVersion uint64
	Summary       *schema.Message
	Retained      []*schema.Message
	CoveredSeq    int64
}

func (c *Conversation) CompactNeeded(context.Context) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.compactor == nil {
		return false
	}
	if limiter, ok := c.compactor.(AutoCompactLimiter); ok {
		return limiter.AutoCompactTokenLimit() > 0 && c.usage.snapshot.CurrentTotal >= limiter.AutoCompactTokenLimit()
	}
	return true
}
func (c *Conversation) Compact(ctx context.Context, runID string) (*ContextCompactedPayload, error) {
	c.mu.Lock()
	if c.compactor == nil {
		c.mu.Unlock()
		return nil, nil
	}
	version := c.version
	messages := append([]*schema.Message(nil), c.messages...)
	covered := c.cursor
	c.mu.Unlock()
	result, err := c.compactor.Compact(ctx, messages)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	if result.Compact == nil || result.Compact.Summary == nil || len(result.Rebuilt) == 0 {
		return nil, fmt.Errorf("invalid compact result")
	}
	// Persist the actual rebuilt window; strategies may retain a tool exchange.
	if result.Rebuilt[0] != result.Compact.Summary {
		return nil, fmt.Errorf("compact result must begin with its summary")
	}
	snapshot := CompactSnapshot{Version: 1, SourceVersion: version, Summary: result.Compact.Summary, Retained: result.Rebuilt[1:], CoveredSeq: covered}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.version != version {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r := c.record(ctx, runID, snapshot.Summary, HistoryRecordCompact)
	r.Ext = &HistoryRecordExtend{CompactStrategyID: "core_snapshot_v1", CompactStrategyPayload: string(raw)}
	if c.store != nil {
		if err := c.store.Append(ctx, r); err != nil {
			return nil, err
		}
	}
	before := c.usage.snapshot
	c.messages = append([]*schema.Message(nil), result.Rebuilt...)
	c.version++
	c.usage.recompute(c.messages)
	if r.MessageID > 0 {
		c.seen[r.MessageID] = struct{}{}
	}
	if r.OrderSeq() > c.cursor {
		c.cursor = r.OrderSeq()
	}
	return &ContextCompactedPayload{StrategyID: c.compactor.ID(), Before: before, After: c.usage.snapshot}, nil
}
func (c *Conversation) restoreCompact(ctx context.Context, r *HistoryRecord) ([]*schema.Message, error) {
	if r.Ext == nil || r.Message == nil {
		return nil, fmt.Errorf("incomplete compact record")
	}
	if r.Ext.CompactStrategyID == "core_snapshot_v1" {
		var snapshot CompactSnapshot
		if err := json.Unmarshal([]byte(r.Ext.CompactStrategyPayload), &snapshot); err != nil {
			return nil, err
		}
		if snapshot.Version != 1 || snapshot.Summary == nil {
			return nil, fmt.Errorf("unsupported compact snapshot version %d", snapshot.Version)
		}
		return append([]*schema.Message{snapshot.Summary}, snapshot.Retained...), nil
	}
	if c.compactor == nil || c.compactor.ID() != r.Ext.CompactStrategyID {
		return nil, fmt.Errorf("cannot restore compaction strategy %q", r.Ext.CompactStrategyID)
	}
	result, err := c.compactor.Resume(ctx, &CompactRecord{Summary: r.Message, CompactStrategyID: r.Ext.CompactStrategyID, CompactStrategyPayload: r.Ext.CompactStrategyPayload}, nil)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("empty compact restore")
	}
	return result.Rebuilt, nil
}
