package runtime

import (
	"context"
	"eino-cli/deepagent/manager/api"
	"eino-cli/deepagent/protocol"
	"sync"
	"time"
)

type RunStream struct {
	inputKind                  protocol.InputKind
	Events                     chan protocol.Event
	manager                    api.Manager
	threadID, messageID, runID string
	cancel                     context.CancelFunc
	sub                        *api.Subscription
	mu                         sync.Mutex
	err                        error
	once                       sync.Once
}

func (s *RunStream) Close() {
	s.once.Do(func() {
		s.cancel()
		if s.sub.Close != nil {
			s.sub.Close()
		}
	})
}
func (s *RunStream) Cancel(ctx context.Context) error {
	return s.manager.Cancel(ctx, s.threadID, s.messageID)
}
func (s *RunStream) Err() error         { s.mu.Lock(); defer s.mu.Unlock(); return s.err }
func (s *RunStream) setError(err error) { s.mu.Lock(); s.err = err; s.mu.Unlock() }
func (s *RunStream) consume(ctx context.Context, cursor int64, interval time.Duration) {
	defer close(s.Events)
	defer s.Close()
	tracker := newEventTracker(s.messageID, s.runID, s.inputKind)
	emit := func(e protocol.Event) bool {
		if e.ThreadID != s.threadID || !tracker.accept(e) {
			return false
		}
		select {
		case s.Events <- e:
			return e.Terminal() || (s.inputKind == protocol.InputCompact && e.Kind == protocol.EventCompacted)
		case <-ctx.Done():
			return true
		}
	}
	catchup := func() (bool, error) {
		for {
			rows, err := s.manager.ListEvents(ctx, api.EventFilter{ThreadID: s.threadID, After: cursor, Limit: 500})
			if err != nil {
				return false, err
			}
			for _, e := range rows {
				if e.Sequence > cursor {
					cursor = e.Sequence
				}
				if emit(e) {
					return true, nil
				}
			}
			if len(rows) < 500 {
				return false, nil
			}
		}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	live, errs := s.sub.Events, s.sub.Errors
	for {
		done, err := catchup()
		if err != nil {
			s.setError(err)
			return
		}
		if done {
			return
		}
		select {
		case <-ctx.Done():
			s.setError(ctx.Err())
			return
		case e, ok := <-live:
			if !ok {
				live = nil
				continue
			}
			// Durable events always pass through ordered SQL catch-up; never advance
			// its cursor from pubsub, which can arrive out of order or skip records.
			if !e.Durable() {
				emit(e)
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				errs = nil
				live = nil
			} // SQL remains authoritative after realtime failure.
		case <-ticker.C:
		}
	}
}
