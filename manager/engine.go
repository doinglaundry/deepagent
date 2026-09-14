// Package manager provides namespace-scoped scheduling backed by MySQL and Redis.
package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"eino-cli/manager/api"
	"eino-cli/protocol"
)

type delivery struct {
	Input         protocol.Input
	AcceptedToken string
	Done          bool
	Cancelled     bool
	RunID         string
}
type record struct {
	legacy     bool
	hasHistory bool
	Thread     api.Thread
	Permit     api.Permit
	Inputs     []delivery
	History    api.History
	Revision   int64
	now        time.Time
	event      *protocol.Event
	events     []*protocol.Event
}
type backend interface {
	create(context.Context, *record) error
	load(context.Context, string) (*record, error)
	update(context.Context, string, func(*record) error) (*record, error)
	list(context.Context, string, bool, int, int) ([]api.Thread, error)
	events(context.Context, api.EventFilter) ([]protocol.Event, error)
	deliver(context.Context, *record) ([]protocol.Input, error)
	publish(context.Context, protocol.Event) error
	subscribe(context.Context, string) (*api.Subscription, error)
	checkpoint(context.Context, string) ([]byte, error)
	putCheckpoint(context.Context, string, []byte) error
	close() error
}
type engine struct {
	namespace string
	store     backend
}

func (m *engine) Close() error { return m.store.close() }
func (m *engine) CreateThread(ctx context.Context, req api.CreateThreadRequest) (api.Thread, error) {
	if strings.TrimSpace(m.namespace) == "" {
		return api.Thread{}, fmt.Errorf("namespace required")
	}
	if req.ParentID != "" {
		p, e := m.GetThread(ctx, req.ParentID)
		if e != nil {
			return api.Thread{}, e
		}
		if req.SessionID == "" {
			req.SessionID = p.SessionID
		}
		if req.SessionID != p.SessionID {
			return api.Thread{}, api.ErrConflict
		}
	}
	if req.SessionID == "" {
		req.SessionID = protocol.NewID("session")
	}
	now := time.Now().UTC()
	r := &record{Thread: api.Thread{ID: protocol.NewID("thread"), Namespace: m.namespace, SessionID: req.SessionID, ParentID: req.ParentID, Name: req.Name, Role: req.Role, WorkDir: req.WorkDir, State: api.Idle, PlanMode: req.PlanMode, CreatedAt: now, UpdatedAt: now}, now: now}
	if req.Input != nil {
		in, err := prepareInput(r, *req.Input)
		if err != nil {
			return api.Thread{}, err
		}
		if in.Kind != protocol.InputUser && in.Kind != protocol.InputCompact {
			return api.Thread{}, api.ErrConflict
		}
		r.Inputs = append(r.Inputs, delivery{Input: in})
		r.Thread.State = api.Ready
	}
	if err := m.store.create(ctx, r); err != nil {
		return api.Thread{}, err
	}
	if len(r.Inputs) > 0 {
		if q, ok := m.store.(interface {
			enqueueInput(context.Context, string) error
		}); ok {
			if err := q.enqueueInput(ctx, r.Inputs[0].Input.ID); err != nil {
				return api.Thread{}, err
			}
		}
	}
	return r.Thread, nil
}
func prepareInput(r *record, in protocol.Input) (protocol.Input, error) {
	if err := in.Validate(); err != nil {
		return protocol.Input{}, err
	}
	if in.ID == "" {
		in.ID = protocol.NewID("input")
	}
	in.ThreadID = r.Thread.ID
	in.SessionID = r.Thread.SessionID
	in.CreatedAt = r.now
	return in, nil
}
func (m *engine) GetThread(ctx context.Context, id string) (api.Thread, error) {
	r, e := m.store.load(ctx, id)
	if e != nil {
		return api.Thread{}, e
	}
	return r.Thread, nil
}
func (m *engine) ListSessionThreads(ctx context.Context, session string, limit, offset int) ([]api.Thread, error) {
	return m.store.list(ctx, session, false, limit, offset)
}
func (m *engine) SubmitInput(ctx context.Context, id string, in protocol.Input) (protocol.Input, error) {
	var out protocol.Input
	_, err := m.store.update(ctx, id, func(r *record) error {
		if r.Thread.State == api.Closing || r.Thread.State == api.Closed {
			return api.ErrClosed
		}
		if in.Kind != protocol.InputUser && in.Kind != protocol.InputCompact {
			return api.ErrConflict
		}
		var e error
		out, e = prepareInput(r, in)
		if e != nil {
			return e
		}
		for _, d := range r.Inputs {
			if d.Input.ID == out.ID {
				if sameInput(d.Input, out) {
					out = d.Input
					return nil
				}
				return api.ErrConflict
			}
		}
		r.Inputs = append(r.Inputs, delivery{Input: out})
		if r.Thread.State == api.Idle {
			r.Thread.State = api.Ready
		}
		return nil
	})
	if err == nil {
		if q, ok := m.store.(interface {
			enqueueInput(context.Context, string) error
		}); ok {
			err = q.enqueueInput(ctx, out.ID)
		}
	}
	return out, err
}
func sameInput(a, b protocol.Input) bool {
	a.CreatedAt = time.Time{}
	b.CreatedAt = time.Time{}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
func (m *engine) ResumeFromBlock(ctx context.Context, id string, in protocol.Input) (protocol.Input, error) {
	var out protocol.Input
	_, err := m.store.update(ctx, id, func(r *record) error {
		if r.Thread.State == api.Closing || r.Thread.State == api.Closed {
			return api.ErrClosed
		}
		if in.Kind != protocol.InputResume || in.Validate() != nil {
			return fmt.Errorf("valid resume input required")
		}
		b := r.Thread.Block
		if r.Thread.State != api.Blocked || b == nil {
			return api.ErrConflict
		}
		if in.Resume.RunID != b.RunID || in.Resume.CheckpointID != b.CheckpointID || in.Resume.InterruptID != b.InterruptID {
			return api.ErrConflict
		}
		var e error
		out, e = prepareInput(r, in)
		if e != nil {
			return e
		}
		for _, d := range r.Inputs {
			if d.Input.ID == out.ID {
				return api.ErrConflict
			}
		}
		// The checkpoint owns already accepted inputs; replay only the correlated resume.
		for i := range r.Inputs {
			if r.Inputs[i].AcceptedToken != "" {
				r.Inputs[i].Done = true
			}
		}
		r.Inputs = append(r.Inputs, delivery{Input: out})
		r.Thread.Block = nil
		r.Thread.State = api.Ready
		return nil
	})
	if err == nil {
		if q, ok := m.store.(interface {
			enqueueInput(context.Context, string) error
		}); ok {
			err = q.enqueueInput(ctx, out.ID)
		}
	}
	return out, err
}
func (m *engine) Cancel(ctx context.Context, id, cutoff string) error {
	var dispositions []*protocol.Event
	_, e := m.store.update(ctx, id, func(r *record) error {
		if r.Thread.State == api.Closing || r.Thread.State == api.Closed {
			return api.ErrClosed
		}
		last := -1
		for i, d := range r.Inputs {
			if d.Input.Kind == protocol.InputUser || d.Input.Kind == protocol.InputResume || d.Input.Kind == protocol.InputCompact {
				if cutoff == "" || d.Input.ID == cutoff {
					last = i
				}
			}
		}
		if cutoff != "" && last < 0 {
			return api.ErrNotFound
		}
		affectsBlock := cutoff == "" && r.Thread.Block != nil
		affected := false
		for i := 0; i <= last; i++ {
			d := r.Inputs[i]
			if d.Input.Kind == protocol.InputCancel || d.Input.Kind == protocol.InputClose {
				continue
			}
			if !d.Done {
				affected = true
			}
			if r.Thread.Block != nil && d.RunID == r.Thread.Block.RunID {
				affectsBlock = true
			}
		}
		// A retry of an old completed cutoff cannot interrupt a newer execution.
		if !affected && !affectsBlock {
			return nil
		}
		if cutoff == "" && last >= 0 {
			cutoff = r.Inputs[last].Input.ID
		}
		dispositions = pendingCancellationEvents(r, last, "Input cancelled before delivery")
		r.events = dispositions
		for i := 0; i <= last; i++ {
			if r.Inputs[i].Input.Kind != protocol.InputCancel && r.Inputs[i].Input.Kind != protocol.InputClose {
				d := &r.Inputs[i]
				if !d.Done || (r.Thread.Block != nil && d.RunID == r.Thread.Block.RunID) {
					d.Done = true
					d.Cancelled = true
				}
			}
		}
		in, _ := prepareInput(r, protocol.Input{Kind: protocol.InputCancel, CutoffID: cutoff})
		r.Inputs = append(r.Inputs, delivery{Input: in})
		r.Thread.Block = nil
		if r.Thread.State == api.Idle || r.Thread.State == api.Blocked {
			r.Thread.State = api.Ready
		}
		return nil
	})
	if e == nil {
		for _, event := range dispositions {
			_ = m.store.publish(ctx, *event)
		}
	}
	return e
}
func (m *engine) RequestThreadClose(ctx context.Context, id string) error {
	var dispositions []*protocol.Event
	_, e := m.store.update(ctx, id, func(r *record) error {
		if r.Thread.State == api.Closed || r.Thread.State == api.Closing {
			return nil
		}
		dispositions = pendingCancellationEvents(r, len(r.Inputs)-1, "Thread closed before input delivery")
		r.events = dispositions
		for i := range r.Inputs {
			r.Inputs[i].Done = true
		}
		in, _ := prepareInput(r, protocol.Input{Kind: protocol.InputClose})
		r.Inputs = append(r.Inputs, delivery{Input: in})
		r.Thread.Block = nil
		r.Thread.State = api.Closing
		return nil
	})
	if e == nil {
		for _, event := range dispositions {
			_ = m.store.publish(ctx, *event)
		}
	}
	return e
}
func (m *engine) SetPlanMode(ctx context.Context, id string, on bool) error {
	_, e := m.store.update(ctx, id, func(r *record) error {
		if r.Thread.State == api.Closing || r.Thread.State == api.Closed {
			return api.ErrClosed
		}
		r.Thread.PlanMode = on
		return nil
	})
	return e
}
func (m *engine) ScanRunnableThreads(ctx context.Context, limit int) ([]api.Thread, error) {
	return m.store.list(ctx, "", true, limit, 0)
}
func validPermit(r *record, p api.Permit) error {
	if p.ThreadID != r.Thread.ID || p.Token == "" || p.Token != r.Permit.Token || p.WorkerID != r.Permit.WorkerID || !r.Permit.ExpiresAt.After(r.now) || (r.Thread.State != api.Running && r.Thread.State != api.Closing) {
		return api.ErrPermitLost
	}
	return nil
}
func (m *engine) ClaimThread(ctx context.Context, id, worker string, ttl time.Duration) (api.Claim, error) {
	if worker == "" || ttl <= 0 {
		return api.Claim{}, fmt.Errorf("worker and positive permit duration required")
	}
	r, e := m.store.update(ctx, id, func(r *record) error {
		if r.Permit.Token != "" && r.Permit.ExpiresAt.After(r.now) {
			return api.ErrConflict
		}
		if r.Thread.State != api.Ready && r.Thread.State != api.Closing && r.Thread.State != api.Running {
			return api.ErrConflict
		}
		// A durable interrupt may have committed just before the old worker crashed.
		// Reconstruct the blocked state without restarting the completed inputs.
		if r.Thread.Block != nil && r.Thread.State == api.Running {
			r.Thread.State = api.Blocked
			r.Permit = api.Permit{}
			return nil
		}
		// Acknowledgement is only receipt. A different owner retries unfinished inputs.
		for i := range r.Inputs {
			if !r.Inputs[i].Done {
				r.Inputs[i].AcceptedToken = ""
			}
		}
		r.Permit = api.Permit{ThreadID: id, WorkerID: worker, Token: protocol.NewID("permit"), ExpiresAt: r.now.Add(ttl)}
		if r.Thread.State != api.Closing {
			r.Thread.State = api.Running
		}
		return nil
	})
	if e != nil {
		return api.Claim{}, e
	}
	if r.Thread.State == api.Blocked {
		return api.Claim{}, api.ErrConflict
	}
	inputs, e := m.store.deliver(ctx, r)
	if e != nil { // Release preparation failure without relying on the cancelled caller context.
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = m.store.update(cleanup, id, func(cur *record) error {
			if er := validPermit(cur, r.Permit); er != nil {
				return er
			}
			cur.Permit = api.Permit{}
			if cur.Thread.State != api.Closing {
				cur.Thread.State = api.Ready
			}
			return nil
		})
		return api.Claim{}, e
	}
	if q, ok := m.store.(interface {
		requeueInput(context.Context, string) error
	}); ok {
		for _, input := range inputs {
			if err := q.requeueInput(ctx, input.ID); err != nil {
				return api.Claim{}, err
			}
		}
	}
	return api.Claim{Thread: r.Thread, Permit: r.Permit, Inputs: inputs}, nil
}
func (m *engine) RenewThreadPermit(ctx context.Context, p api.Permit, ttl time.Duration) (api.Permit, error) {
	if ttl <= 0 {
		return api.Permit{}, fmt.Errorf("positive permit duration required")
	}
	if store, ok := m.store.(interface {
		renewPermit(context.Context, api.Permit, time.Duration) (api.Permit, error)
	}); ok {
		return store.renewPermit(ctx, p, ttl)
	}
	r, e := m.store.update(ctx, p.ThreadID, func(r *record) error {
		if e := validPermit(r, p); e != nil {
			return e
		}
		r.Permit.ExpiresAt = r.now.Add(ttl)
		return nil
	})
	if e != nil {
		return api.Permit{}, e
	}
	return r.Permit, nil
}
func (m *engine) ReadPendingInputs(ctx context.Context, p api.Permit) ([]protocol.Input, error) {
	r, e := m.store.load(ctx, p.ThreadID)
	if e == nil {
		e = validPermit(r, p)
	}
	if e != nil {
		return nil, e
	}
	return m.store.deliver(ctx, r)
}
func (m *engine) ConfirmInputDelivery(ctx context.Context, p api.Permit, id string) error {
	_, e := m.store.update(ctx, p.ThreadID, func(r *record) error {
		if e := validPermit(r, p); e != nil {
			return e
		}
		for i := range r.Inputs {
			if r.Inputs[i].Input.ID == id {
				r.Inputs[i].AcceptedToken = p.Token
				if r.Inputs[i].Input.Kind == protocol.InputCancel || r.Inputs[i].Input.Kind == protocol.InputClose {
					r.Inputs[i].Done = true
				}
				return nil
			}
		}
		return api.ErrNotFound
	})
	if e == nil {
		if q, ok := m.store.(interface {
			completeInput(context.Context, string) error
		}); ok {
			e = q.completeInput(ctx, id)
		}
	}
	return e
}
func pending(r *record) []protocol.Input {
	out := []protocol.Input{}
	for _, d := range r.Inputs {
		if !d.Done && d.AcceptedToken == "" {
			out = append(out, d.Input)
		}
	}
	priority := func(k protocol.InputKind) int {
		switch k {
		case protocol.InputClose:
			return 0
		case protocol.InputCancel:
			return 1
		case protocol.InputResume:
			return 2
		default:
			return 3
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return priority(out[i].Kind) < priority(out[j].Kind) })
	return out
}
func (m *engine) ReleaseThread(ctx context.Context, p api.Permit, release api.Release) error {
	_, e := m.store.update(ctx, p.ThreadID, func(r *record) error {
		if e := validPermit(r, p); e != nil {
			return e
		}
		if release.Block != nil {
			b := release.Block
			if b.RunID == "" || b.CheckpointID == "" || b.InterruptID == "" {
				return fmt.Errorf("block correlation identifiers required")
			}
		}
		r.Permit = api.Permit{}
		if r.Thread.State == api.Closing {
			return nil
		}
		controls := false
		for _, input := range pending(r) {
			if input.Kind == protocol.InputCancel || input.Kind == protocol.InputClose {
				controls = true
			}
		}
		if release.Block == nil {
			release.Block = r.Thread.Block
		}
		cancelledRun, liveRun := false, false
		if release.Block != nil {
			for _, d := range r.Inputs {
				if d.AcceptedToken != p.Token || (d.RunID != "" && d.RunID != release.Block.RunID) || d.Input.Kind == protocol.InputCancel || d.Input.Kind == protocol.InputClose {
					continue
				}
				if d.Cancelled {
					cancelledRun = true
				} else {
					liveRun = true
				}
			}
		}
		if controls || (cancelledRun && !liveRun) {
			release.Block = nil
			r.Thread.Block = nil
		}
		if release.Block != nil {
			r.Thread.Block = release.Block
			for i := range r.Inputs {
				d := &r.Inputs[i]
				if d.AcceptedToken == p.Token && d.RunID == "" && (d.Input.Kind == protocol.InputUser || d.Input.Kind == protocol.InputResume) {
					d.RunID = release.Block.RunID
				}
			}
			r.Thread.State = api.Blocked
			return nil
		}
		// A clean release without a terminal event is also retryable, never silently lost.
		for i := range r.Inputs {
			if !r.Inputs[i].Done {
				r.Inputs[i].AcceptedToken = ""
			}
		}
		r.Thread.State = api.Idle
		if len(pending(r)) > 0 {
			r.Thread.State = api.Ready
		}
		return nil
	})
	return e
}
func (m *engine) ConfirmThreadClosed(ctx context.Context, p api.Permit) error {
	_, e := m.store.update(ctx, p.ThreadID, func(r *record) error {
		if r.Thread.State == api.Closed && r.Permit.Token == p.Token && r.Permit.WorkerID == p.WorkerID {
			return nil
		}
		if e := validPermit(r, p); e != nil {
			return e
		}
		if r.Thread.State != api.Closing {
			return api.ErrConflict
		}
		r.Thread.State = api.Closed
		for i := range r.Inputs {
			r.Inputs[i].Done = true
		}
		return nil
	})
	return e
}
func (m *engine) PublishEvent(ctx context.Context, p api.Permit, event protocol.Event) (protocol.Event, error) {
	if len(event.Data) > 0 && !json.Valid(event.Data) {
		return protocol.Event{}, fmt.Errorf("invalid event JSON")
	}
	if !event.Durable() {
		var r *record
		var err error
		if loader, ok := m.store.(interface {
			loadPermit(context.Context, string) (*record, error)
		}); ok {
			r, err = loader.loadPermit(ctx, p.ThreadID)
		} else {
			r, err = m.store.load(ctx, p.ThreadID)
		}
		if err != nil {
			return protocol.Event{}, err
		}
		if err = validPermit(r, p); err != nil {
			return protocol.Event{}, err
		}
		if event.ID == "" {
			event.ID = protocol.NewID("event")
		}
		event.ThreadID = r.Thread.ID
		event.SessionID = r.Thread.SessionID
		event.Namespace = m.namespace
		event.CreatedAt = r.now
		event.Sequence = 0
		_ = m.store.publish(ctx, event)
		return event, nil
	}
	_, err := m.store.update(ctx, p.ThreadID, func(r *record) error {
		if e := validPermit(r, p); e != nil {
			return e
		}
		if event.ID == "" {
			event.ID = protocol.NewID("event")
		}
		event.ThreadID = r.Thread.ID
		event.SessionID = r.Thread.SessionID
		event.Namespace = m.namespace
		event.CreatedAt = r.now
		event.Sequence = 0
		ids := map[string]bool{}
		for _, id := range event.MessageIDs {
			ids[id] = true
		}
		explicitCancellation := false
		if event.Kind == protocol.EventRunCancelled {
			for _, d := range r.Inputs {
				if d.Cancelled && (ids[d.Input.ID] || (event.RunID != "" && d.RunID == event.RunID)) {
					explicitCancellation = true
					break
				}
			}
		}
		for i := range r.Inputs {
			d := &r.Inputs[i]
			if ids[d.Input.ID] && event.RunID != "" {
				d.RunID = event.RunID
			}
			if (event.Kind == protocol.EventCompacted && ids[d.Input.ID]) || (event.Terminal() && (ids[d.Input.ID] || (event.RunID != "" && d.RunID == event.RunID))) {
				if !explicitCancellation || d.Cancelled {
					d.Done = true
				}
			}
		}
		if explicitCancellation {
			cancelledIDs := map[string]bool{}
			for _, d := range r.Inputs {
				if d.Cancelled {
					cancelledIDs[d.Input.ID] = true
				}
			}
			filtered := make([]string, 0, len(event.MessageIDs))
			for _, id := range event.MessageIDs {
				if cancelledIDs[id] {
					filtered = append(filtered, id)
				}
			}
			event.MessageIDs = filtered
		}
		if event.Kind == protocol.EventBlocked || (event.Kind == protocol.EventRunFailed && event.Block != nil) {
			if event.Block == nil || event.Block.RunID == "" || event.Block.CheckpointID == "" || event.Block.InterruptID == "" {
				return fmt.Errorf("blocked event requires correlation identifiers")
			}
			// Closing/cancellation wins if it arrived while the worker was draining.
			if r.Thread.State != api.Closing {
				relevant := false
				for _, d := range r.Inputs {
					if d.Cancelled {
						continue
					}
					if d.RunID == event.RunID && d.AcceptedToken == p.Token {
						relevant = true
					}
					// Restore failures are durably disposed before delivery ack.
					if event.Kind == protocol.EventRunFailed && ids[d.Input.ID] && d.Input.Kind == protocol.InputResume && d.Input.Resume != nil {
						resume := d.Input.Resume
						b := event.Block
						if resume.RunID == b.RunID && resume.CheckpointID == b.CheckpointID && resume.InterruptID == b.InterruptID && event.RunID == b.RunID {
							relevant = true
						}
					}
				}
				if relevant {
					r.Thread.Block = event.Block
				}
			}
		}
		r.event = &event
		return nil
	})
	if err != nil {
		return protocol.Event{}, err
	}
	// Once durable storage commits, pubsub is only a latency optimization. Consumers
	// reconcile by sequence and event ID, so a publish failure cannot undo acceptance.
	_ = m.store.publish(ctx, event)
	return event, nil
}
func (m *engine) ListEvents(ctx context.Context, f api.EventFilter) ([]protocol.Event, error) {
	return m.store.events(ctx, f)
}
func (m *engine) SubscribeSession(ctx context.Context, session string) (*api.Subscription, error) {
	return m.store.subscribe(ctx, session)
}
func (m *engine) LoadHistory(ctx context.Context, id string) (api.History, error) {
	r, e := m.store.load(ctx, id)
	if e != nil {
		return api.History{}, e
	}
	return r.History, nil
}
func (m *engine) SaveHistory(ctx context.Context, p api.Permit, h api.History) (api.History, error) {
	r, e := m.store.update(ctx, p.ThreadID, func(r *record) error {
		if e := validPermit(r, p); e != nil {
			return e
		}
		if h.Version != r.History.Version {
			return api.ErrConflict
		}
		if len(h.Messages) > 0 && !json.Valid(h.Messages) {
			return fmt.Errorf("invalid messages JSON")
		}
		if len(h.Compactions) > 0 && !json.Valid(h.Compactions) {
			return fmt.Errorf("invalid compactions JSON")
		}
		h.Version++
		r.History = h
		return nil
	})
	if e != nil {
		return api.History{}, e
	}
	return r.History, nil
}
func (m *engine) GetCheckpoint(ctx context.Context, key string) ([]byte, error) {
	return m.store.checkpoint(ctx, key)
}
func (m *engine) PutCheckpoint(ctx context.Context, key string, b []byte) error {
	if key == "" {
		return fmt.Errorf("checkpoint key required")
	}
	return m.store.putCheckpoint(ctx, key, b)
}
func bounds(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func sameEvent(a, b protocol.Event) bool {
	a.CreatedAt = time.Time{}
	b.CreatedAt = time.Time{}
	a.Sequence = 0
	b.Sequence = 0
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// Pending inputs have no runtime to publish their terminal disposition. Group by
// known execution (especially resumes), assigning a synthetic execution only when
// none ever started. Events and receipt cancellation commit in one transaction.
func pendingCancellationEvents(r *record, last int, reason string) []*protocol.Event {
	groups := map[string]*protocol.Event{}
	events := []*protocol.Event{}
	synthetic := ""
	for i := 0; i <= last; i++ {
		d := r.Inputs[i]
		if d.Done || d.AcceptedToken != "" || d.Input.Kind == protocol.InputCancel || d.Input.Kind == protocol.InputClose {
			continue
		}
		runID := d.RunID
		if runID == "" && d.Input.Resume != nil {
			runID = d.Input.Resume.RunID
		}
		if runID == "" {
			if synthetic == "" {
				synthetic = protocol.NewID("run")
			}
			runID = synthetic
		}
		event := groups[runID]
		if event == nil {
			event = &protocol.Event{ID: protocol.NewID("event"), Namespace: r.Thread.Namespace, SessionID: r.Thread.SessionID, ThreadID: r.Thread.ID, RunID: runID, Kind: protocol.EventRunCancelled, Text: reason, CreatedAt: r.now}
			groups[runID] = event
			events = append(events, event)
		}
		event.MessageIDs = append(event.MessageIDs, d.Input.ID)
	}
	return events
}
func recordEvents(r *record) []*protocol.Event {
	events := append([]*protocol.Event(nil), r.events...)
	if r.event != nil {
		events = append(events, r.event)
	}
	return events
}
