// Package agentthread owns conversation history and serializes runs and pending input.
package agentthread

import (
	"context"
	"eino-cli/deepagent/core/compact"
	core "eino-cli/deepagent/core/engine"
	"eino-cli/deepagent/protocol"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"strings"
	"sync"
	"time"
)

var ErrDraining = errors.New("run is draining final events")
var ErrBlocked = errors.New("thread awaits correlated resume input")

type History interface {
	Load(context.Context) ([]*schema.Message, error)
	Save(context.Context, []*schema.Message) error
}
type Checkpoints interface {
	Get(context.Context, string) ([]byte, bool, error)
	Set(context.Context, string, []byte) error
}
type Config struct {
	CompactThresholdTokens                       int
	KeepRecentMessages                           int
	Context                                      context.Context
	Namespace, SessionID, ThreadID, SystemPrompt string
	Model                                        model.ToolCallingChatModel
	SummaryModel                                 model.BaseChatModel
	Tools                                        []tool.BaseTool
	MaxSteps, MaxModelCalls                      int
	PlanMode                                     bool
	ToolPolicy                                   core.ToolPolicy
	History                                      History
	Checkpoints                                  Checkpoints
	// ObserveHistory receives a completed immutable history snapshot for optional long-term memory extraction.
	ObserveHistory func(context.Context, []*schema.Message) error
}
type run struct {
	state     *core.State
	cancel    context.CancelFunc
	pending   []protocol.Input
	accepting bool
	done      chan struct{}
}
type Thread struct {
	c         Config
	mu        sync.Mutex
	history   []*schema.Message
	version   int64
	current   *run
	block     *protocol.Block
	events    chan protocol.Event
	closed    bool
	stop      chan struct{}
	wg        sync.WaitGroup
	closeOnce sync.Once
}

func New(c Config) (*Thread, error) {
	if c.Model == nil {
		return nil, errors.New("model required")
	}
	if c.ThreadID == "" {
		return nil, errors.New("thread ID required")
	}
	if c.SummaryModel != nil && c.CompactThresholdTokens == 0 {
		c.CompactThresholdTokens = 24000
	}
	if c.KeepRecentMessages <= 0 {
		c.KeepRecentMessages = 6
	}
	t := &Thread{c: c, events: make(chan protocol.Event, 256), stop: make(chan struct{})}
	if c.History != nil {
		loadCtx := c.Context
		if loadCtx == nil {
			loadCtx = context.Background()
		}
		loadCtx, cancel := context.WithTimeout(loadCtx, 10*time.Second)
		defer cancel()
		h, e := c.History.Load(loadCtx)
		if e != nil {
			return nil, e
		}
		t.history = h
	}
	if c.SystemPrompt != "" {
		if len(t.history) > 0 && t.history[0].Role == schema.System && !strings.HasPrefix(t.history[0].Content, "Earlier conversation summary:") {
			t.history[0] = schema.SystemMessage(c.SystemPrompt)
		} else {
			t.history = append([]*schema.Message{schema.SystemMessage(c.SystemPrompt)}, t.history...)
		}
	}
	return t, nil
}
func (t *Thread) Events() <-chan protocol.Event { return t.events }
func (t *Thread) emit(s *core.State, e protocol.Event) {
	e.ID = protocol.NewID("event")
	e.Namespace = t.c.Namespace
	e.SessionID = t.c.SessionID
	e.ThreadID = t.c.ThreadID
	e.CreatedAt = time.Now().UTC()
	if s != nil {
		e.RunID = s.RunID
		if len(e.MessageIDs) == 0 {
			e.MessageIDs = append([]string(nil), s.MessageIDs...)
		}
	}
	select {
	case t.events <- e:
	case <-t.stop:
	}
}
func (t *Thread) SubmitInput(ctx context.Context, in protocol.Input) (string, error) {
	if e := in.Validate(); e != nil {
		return "", e
	}
	if in.Kind == protocol.InputResume {
		return t.ResumeRun(ctx, in)
	}
	if in.Kind == protocol.InputCompact {
		return "", t.compact(ctx, in.ID)
	}
	if in.Kind != protocol.InputUser {
		return "", errors.New("SubmitInput requires user input")
	}
	if in.ThreadID != "" && in.ThreadID != t.c.ThreadID {
		return "", errors.New("input belongs to another thread")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return "", errors.New("thread closed")
	}
	if t.block != nil {
		return "", ErrBlocked
	}
	if t.current != nil {
		if !t.current.accepting {
			return "", ErrDraining
		}
		t.current.pending = append(t.current.pending, in)
		return t.current.state.RunID, nil
	}
	s := &core.State{RunID: protocol.NewID("run"), ThreadID: t.c.ThreadID, Namespace: t.c.Namespace, Messages: repairHistory(t.history)}
	r := t.startLocked(ctx, s, []protocol.Input{in})
	return r.state.RunID, nil
}
func (t *Thread) startLocked(ctx context.Context, s *core.State, pending []protocol.Input) *run {
	ctx, cancel := context.WithCancel(ctx)
	r := &run{state: s, cancel: cancel, pending: pending, accepting: true, done: make(chan struct{})}
	t.current = r
	t.wg.Add(1)
	go t.execute(ctx, r)
	return r
}
func (t *Thread) beforeModel(ctx context.Context, r *run) error {
	t.mu.Lock()
	pending := r.pending
	r.pending = nil
	t.mu.Unlock()
	for _, in := range pending {
		r.state.MessageIDs = append(r.state.MessageIDs, in.ID)
		m := inputMessage(in)
		if !hasInput(r.state.Messages, in.ID) {
			r.state.Messages = append(r.state.Messages, m)
		}
		t.emit(r.state, protocol.Event{Kind: protocol.EventInputConsumed, MessageIDs: []string{in.ID}, Text: m.Content})
	}
	if e := t.autoCompact(ctx, r.state); e != nil {
		return e
	}
	return t.save(ctx, r.state)
}
func (t *Thread) shouldContinue(r *run) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(r.pending) > 0 {
		return true
	}
	r.accepting = false
	return false
}
func (t *Thread) save(ctx context.Context, s *core.State) error {
	if t.c.History != nil {
		if e := t.c.History.Save(ctx, s.Messages); e != nil {
			return e
		}
	}
	t.mu.Lock()
	t.history = append([]*schema.Message(nil), s.Messages...)
	t.version++
	t.mu.Unlock()
	if s.CheckpointID != "" {
		if t.c.Checkpoints == nil {
			return errors.New("approval/clarification requires durable checkpoint storage")
		}
		b, e := json.Marshal(s)
		if e != nil {
			return e
		}
		if e = t.c.Checkpoints.Set(ctx, s.CheckpointID, b); e != nil {
			return fmt.Errorf("persist checkpoint: %w", e)
		}
	}
	return nil
}
func (t *Thread) execute(ctx context.Context, r *run) {
	defer t.wg.Done()
	defer close(r.done)
	defer r.cancel()
	s := r.state
	t.mu.Lock()
	ids := append([]string(nil), s.MessageIDs...)
	for _, in := range r.pending {
		ids = append(ids, in.ID)
	}
	t.mu.Unlock()
	t.emit(s, protocol.Event{Kind: protocol.EventRunStarted, MessageIDs: ids})
	a, e := core.New(ctx, core.Config{Model: t.c.Model, Tools: t.c.Tools, MaxSteps: t.c.MaxSteps, MaxModelCalls: t.c.MaxModelCalls, PlanMode: t.c.PlanMode, ToolPolicy: t.c.ToolPolicy, Emit: func(ev protocol.Event) { t.emit(s, ev) }, BeforeModel: func(ctx context.Context, _ *core.State) error { return t.beforeModel(ctx, r) }, Continue: func(*core.State) bool { return t.shouldContinue(r) }, Save: t.save})
	if e == nil {
		_, e = a.Run(ctx, s)
	}
	t.mu.Lock()
	r.accepting = false
	pending := r.pending
	r.pending = nil
	t.mu.Unlock()
	if ctx.Err() != nil && e == nil {
		e = ctx.Err()
	}
	if e != nil {
		repairToolPairs(s)
	}
	if e == nil && s.Block != nil {
		s.Pending = append(s.Pending, pending...)
		pending = nil
	}
	// Inputs accepted during tools or cancellation remain recorded even if no next model call occurs.
	for _, in := range pending {
		if !hasInput(s.Messages, in.ID) {
			s.Messages = append(s.Messages, inputMessage(in))
		}
		s.MessageIDs = append(s.MessageIDs, in.ID)
		t.emit(s, protocol.Event{Kind: protocol.EventInputConsumed, MessageIDs: []string{in.ID}, Text: inputMessage(in).Content})
	}
	if e != nil {
		repairToolPairs(s)
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	if se := t.save(saveCtx, s); se != nil && e == nil {
		e = se
	}
	cancel()
	kind := protocol.EventRunCompleted
	if e != nil {
		kind = protocol.EventRunFailed
	}
	if ctx.Err() != nil {
		kind = protocol.EventRunCancelled
	}
	if e == nil && s.Block != nil {
		kind = protocol.EventBlocked
		t.mu.Lock()
		t.block = s.Block
		t.mu.Unlock()
	}
	ev := protocol.Event{Kind: kind, Block: s.Block}
	if e != nil {
		ev.Error = e.Error()
	}
	t.emit(s, ev)
	// All events are forwarded to the thread channel before a new run can replace current.
	t.mu.Lock()
	if t.current == r {
		t.current = nil
	}
	t.mu.Unlock()
	if kind == protocol.EventRunCompleted && t.c.ObserveHistory != nil {
		m := cloneMessages(s.Messages)
		mc, cc := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		_ = t.c.ObserveHistory(mc, m)
		cc()
	}
}
func repairToolPairs(s *core.State) {
	for i := s.ToolIndex; i < len(s.Calls); i++ {
		c := s.Calls[i]
		s.Messages = append(s.Messages, &schema.Message{Role: schema.Tool, ToolCallID: c.ID, Name: c.Function.Name, Content: "Tool execution interrupted; external side effects may have occurred."})
	}
	s.Calls = nil
	s.ToolIndex = 0
	s.Block = nil
}
func (t *Thread) ResumeRun(ctx context.Context, in protocol.Input) (string, error) {
	if e := in.Validate(); e != nil {
		return "", e
	}
	if in.Kind != protocol.InputResume {
		return "", errors.New("resume input required")
	}
	if t.c.Checkpoints == nil {
		return "", errors.New("checkpoint storage required")
	}
	b, exists, e := t.c.Checkpoints.Get(ctx, in.Resume.CheckpointID)
	if e != nil {
		return "", e
	}
	if !exists {
		return "", errors.New("checkpoint not found")
	}
	var s core.State
	if e = json.Unmarshal(b, &s); e != nil {
		return "", e
	}
	if s.ThreadID != t.c.ThreadID || s.Namespace != t.c.Namespace || s.Block == nil || s.RunID != in.Resume.RunID || s.Block.CheckpointID != in.Resume.CheckpointID || s.Block.InterruptID != in.Resume.InterruptID {
		return "", errors.New("resume correlation mismatch")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return "", errors.New("thread closed")
	}
	if t.current != nil {
		return "", ErrDraining
	}
	s.Resume = in.Resume
	s.MessageIDs = append(s.MessageIDs, in.ID)
	t.block = nil
	pending := s.Pending
	s.Pending = nil
	t.startLocked(ctx, &s, pending)
	return s.RunID, nil
}
func (t *Thread) Cancel() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current != nil {
		t.current.accepting = false
		t.current.cancel()
	}
	t.block = nil
}
func (t *Thread) Close() error {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.closed = true
		if t.current != nil {
			t.current.cancel()
		}
		t.mu.Unlock()
		t.wg.Wait()
		close(t.stop)
		close(t.events)
	})
	return nil
}
func cloneMessages(m []*schema.Message) []*schema.Message {
	b, _ := json.Marshal(m)
	var out []*schema.Message
	_ = json.Unmarshal(b, &out)
	return out
}

// Compact summarizes outside the mutex and commits only if the history version is unchanged.
func (t *Thread) Compact(ctx context.Context) error { return t.compact(ctx, "") }
func (t *Thread) compact(ctx context.Context, inputID string) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return errors.New("thread closed")
	}
	if t.block != nil {
		t.mu.Unlock()
		return ErrBlocked
	}
	t.wg.Add(1)
	defer t.wg.Done()
	version := t.version
	messages := cloneMessages(t.history)
	t.mu.Unlock()
	m := t.c.SummaryModel
	if m == nil {
		m = t.c.Model
	}
	out, record, err := compact.Summarize(ctx, m, messages, t.c.KeepRecentMessages)
	if err != nil {
		return err
	}
	if record.Removed == 0 {
		t.emit(nil, protocol.Event{Kind: protocol.EventCompacted, MessageIDs: []string{inputID}, Text: "History is already short; no compaction needed."})
		return nil
	}
	t.mu.Lock()
	if t.version != version || t.current != nil || t.block != nil {
		t.mu.Unlock()
		return compact.ErrStale
	}
	if t.c.History != nil {
		if h, ok := t.c.History.(interface {
			SaveCompacted(context.Context, []*schema.Message, compact.Record) error
		}); ok {
			err = h.SaveCompacted(ctx, out, record)
		} else {
			err = t.c.History.Save(ctx, out)
		}
	}
	if err == nil {
		t.history = out
		t.version++
	}
	t.mu.Unlock()
	if err != nil {
		return err
	}
	data, _ := json.Marshal(record)
	t.emit(nil, protocol.Event{Kind: protocol.EventCompacted, Data: data, MessageIDs: []string{inputID}})
	return nil
}

func inputMessage(in protocol.Input) *schema.Message {
	m := schema.UserMessage(in.Text)
	if in.ID != "" {
		m.Extra = map[string]any{"deepagent_input_id": in.ID}
	}
	if len(in.Parts) == 0 {
		return m
	}
	if in.Text != "" {
		m.UserInputMultiContent = append(m.UserInputMultiContent, schema.MessageInputPart{Type: schema.ChatMessagePartTypeText, Text: in.Text})
	}
	for _, p := range in.Parts {
		common := schema.MessagePartCommon{URL: &p.URL, MIMEType: p.MIMEType}
		part := schema.MessageInputPart{}
		switch p.Type {
		case "image", "image_url":
			part.Type = schema.ChatMessagePartTypeImageURL
			part.Image = &schema.MessageInputImage{MessagePartCommon: common}
		case "audio", "audio_url":
			part.Type = schema.ChatMessagePartTypeAudioURL
			part.Audio = &schema.MessageInputAudio{MessagePartCommon: common}
		case "video", "video_url":
			part.Type = schema.ChatMessagePartTypeVideoURL
			part.Video = &schema.MessageInputVideo{MessagePartCommon: common}
		case "file", "file_url":
			part.Type = schema.ChatMessagePartTypeFileURL
			part.File = &schema.MessageInputFile{MessagePartCommon: common}
		default:
			part.Type = schema.ChatMessagePartTypeText
			part.Text = p.Text
			if m.Content != "" && p.Text != "" {
				m.Content += "\n"
			}
			m.Content += p.Text
		}
		m.UserInputMultiContent = append(m.UserInputMultiContent, part)
	}
	return m
}

// repairHistory closes incomplete tool exchanges after cancellation or interrupted recovery.
// Approval resume uses its checkpoint state and never passes through this repair path.
func repairHistory(messages []*schema.Message) []*schema.Message {
	var out []*schema.Message
	var pending []schema.ToolCall
	seen := map[string]bool{}
	flush := func() {
		for _, c := range pending {
			if !seen[c.ID] {
				out = append(out, &schema.Message{Role: schema.Tool, ToolCallID: c.ID, Name: c.Function.Name, Content: "Previous tool execution was interrupted. Do not assume the action completed."})
			}
		}
		pending = nil
		seen = map[string]bool{}
	}
	for _, m := range messages {
		if m.Role != schema.Tool {
			flush()
		}
		out = append(out, m)
		if m.Role == schema.Tool {
			seen[m.ToolCallID] = true
		}
		if m.Role == schema.Assistant && len(m.ToolCalls) > 0 {
			pending = m.ToolCalls
		}
	}
	flush()
	return out
}

func (t *Thread) autoCompact(ctx context.Context, s *core.State) error {
	if t.c.CompactThresholdTokens <= 0 || t.c.SummaryModel == nil {
		return nil
	}
	raw, e := json.Marshal(s.Messages)
	if e != nil {
		return e
	}
	if (len(raw)+3)/4 < t.c.CompactThresholdTokens {
		return nil
	}
	t.mu.Lock()
	version := t.version
	t.mu.Unlock()
	out, record, e := compact.Summarize(ctx, t.c.SummaryModel, s.Messages, t.c.KeepRecentMessages)
	if e != nil {
		return e
	}
	if record.Removed == 0 {
		return nil
	}
	t.mu.Lock()
	if t.version != version {
		t.mu.Unlock()
		return compact.ErrStale
	}
	if h, ok := t.c.History.(interface {
		SaveCompacted(context.Context, []*schema.Message, compact.Record) error
	}); ok {
		e = h.SaveCompacted(ctx, out, record)
	}
	if e == nil {
		s.Messages = out
		t.history = append([]*schema.Message(nil), out...)
		t.version++
	}
	t.mu.Unlock()
	if e != nil {
		return e
	}
	data, _ := json.Marshal(record)
	t.emit(s, protocol.Event{Kind: protocol.EventCompacted, Data: data, Text: "Earlier context summarized."})
	return nil
}

func hasInput(messages []*schema.Message, id string) bool {
	if id == "" {
		return false
	}
	for _, m := range messages {
		if m.Extra != nil && m.Extra["deepagent_input_id"] == id {
			return true
		}
		switch ids := m.Extra["deepagent_input_ids"].(type) {
		case []string:
			for _, v := range ids {
				if v == id {
					return true
				}
			}
		case []any:
			for _, v := range ids {
				if v == id {
					return true
				}
			}
		}
	}
	return false
}
