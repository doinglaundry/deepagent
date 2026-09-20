package manager

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"time"

	"eino-cli/deepagent/dal/cache"
	"eino-cli/deepagent/dal/model"
)

func IDNextSharedID(ctx context.Context, counter cache.RedisClient) (int64, error) {
	if counter == nil {
		return 0, ErrRedisUnavailable
	}
	seq, err := counter.IncrBy(ctx, "deepagent:coordinator:global_id", 1)
	if err != nil {
		return 0, err
	}
	const base int64 = 2_000_000_000_000_000_000
	if seq <= 0 || seq > int64(^uint64(0)>>1)-base {
		return 0, errors.New("distributed ID counter overflow")
	}
	return base + seq, nil
}

func createThread(req SubmitRequest, id int64, _ time.Time) *model.Thread {
	metadata := maps.Clone(req.Metadata)
	if metadata == nil {
		metadata = map[string]string{}
	}
	var profile *model.Profile
	if req.Profile != nil && *req.Profile != (model.Profile{}) {
		copy := *req.Profile
		profile = &copy
	}
	return &model.Thread{ThreadID: id, UserID: req.UserID, SessionID: req.SessionID, Status: model.ThreadStatusIdle, Metadata: metadata, Profile: profile}
}

func normalizePermitDuration(ms int64) time.Duration {
	if ms <= 0 {
		return defaultPermitDuration
	}
	if ms > int64(maxPermitDuration/time.Millisecond) {
		return maxPermitDuration
	}
	return time.Duration(ms) * time.Millisecond
}

func RedisPendingInputKey(threadID int64) string { return fmt.Sprintf("ac:thread:%d:input", threadID) }
func RedisAcceptedInputKey(threadID int64) string {
	return fmt.Sprintf("deepagent:thread:%d:accepted", threadID)
}

func cloneEvents(frames []OutputFrame) []OutputFrame {
	out := make([]OutputFrame, len(frames))
	for i := range frames {
		out[i] = frames[i]
		out[i].Payload = append([]byte(nil), frames[i].Payload...)
		out[i].Metadata = maps.Clone(frames[i].Metadata)
	}
	return out
}

func messageIDMember(id int64) string { return strconv.FormatInt(id, 10) }
