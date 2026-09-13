package manager

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"time"
)

type ThreadStatus string

const (
	StatusIdle    ThreadStatus = "idle"
	StatusReady   ThreadStatus = "ready"
	StatusRunning ThreadStatus = "running"
	StatusBlocked ThreadStatus = "blocked"
	StatusClosing ThreadStatus = "closing"
	StatusClosed  ThreadStatus = "closed"
)

type Session struct{ ID, UserID string }
type Thread struct {
	ID, SessionID string
	Status        ThreadStatus
	UpdatedAt     time.Time
	Inputs        [][]byte
}
type ResumeRequest struct{ Input []byte }

type Store interface {
	CreateThread(context.Context, *Thread) error
	GetThread(context.Context, string) (*Thread, error)
	ListThreads(context.Context, string) ([]*Thread, error)
	UpdateThread(context.Context, *Thread) error
}

type Queue interface {
	Enqueue(context.Context, string, []byte) error
	Has(context.Context, string) (bool, error)
}

type Canceler interface {
	Cancel(context.Context, string) error
}

type Event struct {
	ID, ThreadID, RunID, Type string
	Payload                   []byte
	CreatedAt                 time.Time
}
type EventLog interface {
	Append(context.Context, Event) error
}
type EventFanout interface {
	Publish(context.Context, Event) error
}

type Manager struct {
	store    Store
	queue    Queue
	eventLog EventLog
	fanout   EventFanout
}

// LeasedController is the optional fencing-aware controller used by
// distributed workers. The basic Controller remains usable for local runs.
type LeasedController interface {
	ClaimLease(context.Context, string, string, time.Duration) (any, string, error)
	RenewLease(context.Context, string, string, time.Duration) error
	ReleaseLease(context.Context, string, string) error
}

func New(store Store, queue Queue) *Manager { return &Manager{store: store, queue: queue} }
func (m *Manager) WithEvents(log EventLog, fanout EventFanout) *Manager {
	m.eventLog, m.fanout = log, fanout
	return m
}

func (m *Manager) CreateThread(ctx context.Context, sessionID string) (*Thread, error) {
	if m == nil || m.store == nil {
		return nil, errors.New("manager store is required")
	}
	t := &Thread{ID: newID(), SessionID: sessionID, Status: StatusIdle, UpdatedAt: time.Now()}
	if err := m.store.CreateThread(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}
func (m *Manager) GetThread(ctx context.Context, id string) (*Thread, error) {
	return m.store.GetThread(ctx, id)
}
func (m *Manager) ListThreads(ctx context.Context, sessionID string) ([]*Thread, error) {
	return m.store.ListThreads(ctx, sessionID)
}

func (m *Manager) SubInput(ctx context.Context, id string, input []byte) error {
	t, err := m.GetThread(ctx, id)
	if err != nil {
		return err
	}
	if t.Status == StatusClosing || t.Status == StatusClosed || t.Status == StatusBlocked {
		return errors.New("thread is not accepting input")
	}
	if m.queue != nil {
		if err := m.queue.Enqueue(ctx, id, input); err != nil {
			return err
		}
	}
	t.Status = StatusReady
	t.UpdatedAt = time.Now()
	return m.store.UpdateThread(ctx, t)
}
func (m *Manager) Resume(ctx context.Context, id string, req *ResumeRequest) error {
	t, err := m.GetThread(ctx, id)
	if err != nil {
		return err
	}
	if t.Status != StatusBlocked {
		return errors.New("thread is not blocked")
	}
	ready := req != nil && len(req.Input) > 0
	if !ready && m.queue != nil {
		ready, err = m.queue.Has(ctx, id)
		if err != nil {
			return err
		}
	}
	if ready && req != nil && len(req.Input) > 0 && m.queue != nil {
		if err := m.queue.Enqueue(ctx, id, req.Input); err != nil {
			return err
		}
	}
	if ready {
		t.Status = StatusReady
	} else {
		t.Status = StatusIdle
	}
	t.UpdatedAt = time.Now()
	return m.store.UpdateThread(ctx, t)
}
func (m *Manager) Cancel(ctx context.Context, id, reason string) error {
	t, err := m.GetThread(ctx, id)
	if err != nil {
		return err
	}
	if t.Status == StatusClosing || t.Status == StatusClosed || t.Status == StatusBlocked {
		return errors.New("thread cannot be canceled")
	}
	if reason == "" {
		reason = "user cancel"
	}
	if c, ok := m.queue.(Canceler); ok {
		_ = c.Cancel(ctx, id)
	}
	t.Status = StatusClosing
	t.UpdatedAt = time.Now()
	if err := m.store.UpdateThread(ctx, t); err != nil {
		return err
	}
	return m.PublishEvent(ctx, Event{ID: newID(), ThreadID: id, Type: "thread.canceled", Payload: []byte(reason), CreatedAt: time.Now()})
}

func (m *Manager) PublishEvent(ctx context.Context, event Event) error {
	if m.eventLog != nil {
		if err := m.eventLog.Append(ctx, event); err != nil {
			return err
		}
	}
	if m.fanout != nil {
		_ = m.fanout.Publish(ctx, event)
	}
	return nil
}

func (m *Manager) Scan(ctx context.Context, limit int) ([]string, error) {
	threads, err := m.store.ListThreads(ctx, "")
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, limit)
	for _, t := range threads {
		if t.Status == StatusReady {
			ids = append(ids, t.ID)
			if limit > 0 && len(ids) >= limit {
				break
			}
		}
	}
	return ids, nil
}
func (m *Manager) Claim(ctx context.Context, id string) (any, error) {
	t, err := m.GetThread(ctx, id)
	if err != nil {
		return nil, err
	}
	if t.Status != StatusReady {
		return nil, errors.New("thread is not ready")
	}
	t.Status = StatusRunning
	t.UpdatedAt = time.Now()
	if err := m.store.UpdateThread(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

func (m *Manager) ClaimLease(ctx context.Context, id, workerID string, ttl time.Duration) (any, string, error) {
	q, ok := m.queue.(*RedisQueue)
	if !ok || q == nil {
		item, err := m.Claim(ctx, id)
		return item, "", err
	}
	token, err := q.AcquireLease(ctx, id, workerID, ttl)
	if err != nil {
		return nil, "", err
	}
	item, err := m.Claim(ctx, id)
	if err != nil {
		_ = q.ReleaseLease(ctx, id, token)
		return nil, "", err
	}
	return item, token, nil
}
func (m *Manager) RenewLease(ctx context.Context, id, token string, ttl time.Duration) error {
	if q, ok := m.queue.(*RedisQueue); ok {
		return q.RenewLease(ctx, id, token, ttl)
	}
	return nil
}
func (m *Manager) ReleaseLease(ctx context.Context, id, token string) error {
	if q, ok := m.queue.(*RedisQueue); ok {
		if err := q.ReleaseLease(ctx, id, token); err != nil {
			return err
		}
	}
	return m.Release(ctx, id)
}

// AcceptInput/ConfirmInput/RecoverAccepted expose the at-least-once delivery
// ledger without coupling callers to the Redis implementation.
func (m *Manager) AcceptInput(ctx context.Context, threadID, inputID string, payload []byte) error {
	q, ok := m.queue.(*RedisQueue)
	if !ok {
		return nil
	}
	return q.Accept(ctx, threadID, InputLedger{ID: inputID, Payload: payload})
}
func (m *Manager) ConfirmInput(ctx context.Context, threadID, inputID string) error {
	q, ok := m.queue.(*RedisQueue)
	if !ok {
		return nil
	}
	return q.Confirm(ctx, threadID, inputID)
}
func (m *Manager) RecoverAccepted(ctx context.Context, threadID string) error {
	q, ok := m.queue.(*RedisQueue)
	if !ok {
		return nil
	}
	return q.Recover(ctx, threadID)
}
func (m *Manager) Release(ctx context.Context, id string) error {
	t, err := m.GetThread(ctx, id)
	if err != nil {
		return err
	}
	if t.Status == StatusRunning {
		t.Status = StatusIdle
		t.UpdatedAt = time.Now()
		return m.store.UpdateThread(ctx, t)
	}
	return nil
}

func newID() string { return uuid.NewString() }
