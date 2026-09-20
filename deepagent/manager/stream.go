package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"eino-cli/deepagent/dal/cache"
	"github.com/redis/go-redis/v9"
)

type StreamStreamOut struct{ redis cache.RedisClient }

type SubscribeSessionRequest struct {
	SessionID      string
	RecoverQueueID string
}

type Subscription struct {
	Events <-chan OutputFrame
	Err    error
	Close  func() error
}

type streamEnvelope struct {
	Seq   int64       `json:"seq"`
	Frame OutputFrame `json:"frame"`
}

func sessionEventKey(sessionID string) string { return "deepagent:session:" + sessionID + ":events" }
func sessionChannel(sessionID string) string  { return "deepagent:session:" + sessionID + ":live" }

func (s *StreamStreamOut) FanoutEventRecords(ctx context.Context, sessionID string, frames []OutputFrame) error {
	for _, frame := range frames {
		seq, err := s.redis.IncrBy(ctx, sessionEventKey(sessionID)+":seq", 1)
		if err != nil {
			return err
		}
		frame.QueueID = strconv.FormatInt(seq, 10)
		envelope, err := json.Marshal(streamEnvelope{Seq: seq, Frame: frame})
		if err != nil {
			return err
		}
		key := fmt.Sprintf("%s:%d", sessionEventKey(sessionID), seq)
		if err = s.redis.SetRaw(ctx, key, envelope, 24*time.Hour); err != nil {
			return err
		}
		if _, err = s.redis.ZAdd(ctx, sessionEventKey(sessionID), []redis.Z{{Score: float64(seq), Member: strconv.FormatInt(seq, 10)}}); err != nil {
			return err
		}
		if err = s.redis.Publish(ctx, sessionChannel(sessionID), envelope); err != nil {
			return err
		}
	}
	return nil
}

func newSubscription(ctx context.Context, stream *StreamStreamOut, req SubscribeSessionRequest, maxIdle time.Duration) *Subscription {
	events := make(chan OutputFrame, 32)
	subCtx, cancel := context.WithCancel(ctx)
	raw, closePubSub, err := stream.redis.Subscribe(subCtx, sessionChannel(req.SessionID))
	if err != nil {
		cancel()
		close(events)
		return &Subscription{Events: events, Err: err, Close: func() error { return nil }}
	}
	closed := make(chan struct{})
	go func() {
		defer close(events)
		defer close(closed)
		defer closePubSub()
		defer cancel()
		last, _ := strconv.ParseInt(req.RecoverQueueID, 10, 64)
		send := func(data []byte) bool {
			var item streamEnvelope
			if json.Unmarshal(data, &item) != nil || item.Seq <= last {
				return true
			}
			select {
			case events <- item.Frame:
				last = item.Seq
				return true
			case <-subCtx.Done():
				return false
			}
		}
		members, err := stream.redis.ZRange(subCtx, sessionEventKey(req.SessionID), 0, -1)
		if err == nil {
			for _, member := range members {
				seq, parseErr := strconv.ParseInt(member, 10, 64)
				if parseErr != nil || seq <= last {
					continue
				}
				data, getErr := stream.redis.GetRaw(subCtx, fmt.Sprintf("%s:%d", sessionEventKey(req.SessionID), seq))
				if getErr == nil && !send(data) {
					return
				}
			}
		}
		var idle <-chan time.Time
		var timer *time.Timer
		if maxIdle > 0 {
			timer = time.NewTimer(maxIdle)
			defer timer.Stop()
			idle = timer.C
		}
		for {
			select {
			case data, ok := <-raw:
				if !ok {
					return
				}
				if !send(data) {
					return
				}
				if timer != nil {
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(maxIdle)
				}
			case <-idle:
				return
			case <-subCtx.Done():
				return
			}
		}
	}()
	return &Subscription{Events: events, Close: func() error { cancel(); <-closed; return nil }}
}
