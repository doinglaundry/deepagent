package distributed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"

	"eino-cli/deepagent/core/runtime/agentthread"
	"eino-cli/deepagent/manager/api"
	corethread "eino-cli/deepagent/thread"
	"github.com/cloudwego/eino/schema"
)

// rolloutRecords converts a legacy effective context only when no canonical
// log exists. The next successful fenced Append commits that conversion.
func rolloutRecords(h api.History, threadID string) ([]*agentthread.HistoryRecord, error) {
	var records []*agentthread.HistoryRecord
	if hasRollout(h.Rollout) {
		if err := json.Unmarshal(h.Rollout, &records); err != nil {
			return nil, err
		}
	} else {
		var messages []*schema.Message
		if len(h.Messages) > 0 {
			if err := json.Unmarshal(h.Messages, &messages); err != nil {
				return nil, err
			}
		}
		for _, message := range messages {
			if message == nil {
				continue
			}
			seq := int64(len(records) + 1)
			key := inputRecordKey(message)
			if key == "" {
				key = fmt.Sprintf("legacy/%d", seq)
			}
			records = append(records, &agentthread.HistoryRecord{Type: "message", ThreadID: threadID, MessageID: seq, Seq: seq, Message: message, UniqueKey: key})
		}
	}
	var seq int64
	for _, r := range records {
		if r == nil || r.ThreadID != threadID || r.Seq <= seq || r.MessageID <= 0 {
			return nil, fmt.Errorf("invalid history rollout identity or sequence")
		}
		seq = r.Seq
	}
	return records, nil
}

func (h *History) List(ctx context.Context, q agentthread.ListQuery) ([]*agentthread.HistoryRecord, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if q.ThreadID != "" && q.ThreadID != h.permit.ThreadID {
		return nil, fmt.Errorf("history belongs to another thread")
	}
	stored, err := h.read(ctx)
	if err != nil {
		return nil, err
	}
	records, err := rolloutRecords(stored, h.permit.ThreadID)
	if err != nil {
		return nil, err
	}
	var out []*agentthread.HistoryRecord
	for i := 0; i < len(records); i++ {
		index := i
		if q.Order == agentthread.ListOrderDESC {
			index = len(records) - 1 - i
		}
		r := records[index]
		if q.RunID != "" && r.RunID != q.RunID {
			continue
		}
		if q.Order == agentthread.ListOrderDESC {
			if q.BeforeID != nil && r.Seq >= *q.BeforeID {
				continue
			}
		} else if q.AfterID != nil && r.Seq <= *q.AfterID {
			continue
		}
		out = append(out, r)
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
	}
	h.history = stored
	return out, nil
}

func (h *History) Append(ctx context.Context, rec *agentthread.HistoryRecord) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if rec == nil || rec.ThreadID != h.permit.ThreadID || rec.Message == nil {
		return fmt.Errorf("invalid history record")
	}
	stored, err := h.read(ctx)
	if err != nil {
		return err
	}
	records, err := rolloutRecords(stored, h.permit.ThreadID)
	if err != nil {
		return err
	}
	key := rec.UniqueKey
	if rec.Type == "message" {
		if stable := inputRecordKey(rec.Message); stable != "" {
			key = stable
		}
	}
	var maxID, seq int64
	for _, r := range records {
		storedKey := r.UniqueKey
		if r.Type == "message" {
			if stable := inputRecordKey(r.Message); stable != "" {
				storedKey = stable
			}
		}
		if (rec.MessageID > 0 && rec.MessageID == r.MessageID) || (key != "" && key == storedKey) {
			original, _ := json.Marshal(r.Message)
			incoming, err := json.Marshal(rec.Message)
			if err != nil {
				return err
			}
			oldExt, _ := json.Marshal(r.Ext)
			newExt, extErr := json.Marshal(rec.Ext)
			if extErr != nil {
				return extErr
			}
			if string(original) != string(incoming) || r.Type != rec.Type || string(oldExt) != string(newExt) {
				return fmt.Errorf("history identity reused for different content")
			}
			rec.MessageID, rec.Seq, rec.UniqueKey = r.MessageID, r.Seq, r.UniqueKey
			return nil
		}
		if r.MessageID > maxID {
			maxID = r.MessageID
		}
		seq = r.Seq
	}
	if seq == math.MaxInt64 || (rec.MessageID == 0 && maxID == math.MaxInt64) {
		return fmt.Errorf("history sequence exhausted")
	}
	copy := *rec
	copy.UniqueKey = key
	copy.Seq = seq + 1
	if copy.MessageID == 0 {
		copy.MessageID = maxID + 1
	}
	if copy.MessageID < 0 {
		return fmt.Errorf("invalid message ID")
	}
	var messages []*schema.Message
	if len(stored.Messages) > 0 {
		if err = json.Unmarshal(stored.Messages, &messages); err != nil {
			return err
		}
	}
	switch copy.Type {
	case "message":
		messages = append(messages, copy.Message)
	case "compact":
		if copy.Ext == nil || copy.Ext.CompactStrategyID != "core_snapshot_v1" {
			return fmt.Errorf("unsupported rollout compaction")
		}
		var snapshot agentthread.CompactSnapshot
		if err = json.Unmarshal([]byte(copy.Ext.CompactStrategyPayload), &snapshot); err != nil {
			return err
		}
		if snapshot.Version != 1 || snapshot.Summary == nil || snapshot.CoveredSeq != seq {
			return fmt.Errorf("invalid or stale compaction snapshot")
		}
		messages = append([]*schema.Message{snapshot.Summary}, snapshot.Retained...)
	default:
		return fmt.Errorf("unsupported history record type %q", copy.Type)
	}
	records = append(records, &copy)
	stored.Rollout, err = json.Marshal(records)
	if err != nil {
		return err
	}
	stored.Messages, err = json.Marshal(messages)
	if err != nil {
		return err
	}
	next, err := h.manager.SaveHistory(ctx, h.permit, stored)
	if err != nil {
		return err
	}
	h.history = next
	rec.MessageID, rec.Seq, rec.UniqueKey = copy.MessageID, copy.Seq, copy.UniqueKey
	return nil
}

var _ agentthread.HistoryRolloutStore = (*History)(nil)

func hasRollout(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && !bytes.Equal(raw, []byte("null"))
}

// Transport message identity survives worker replacement and per-run record keys.
func inputRecordKey(message *schema.Message) string {
	if message == nil || message.Role != schema.User {
		return ""
	}
	id := corethread.MessageID(message)
	if id == "" && message.Extra != nil {
		id, _ = message.Extra["deepagent_input_id"].(string)
	}
	if id == "" {
		return ""
	}
	return "input/" + id
}
