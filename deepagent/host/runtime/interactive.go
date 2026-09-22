package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	legacy "eino-cli/deepagent/host/runtime/local"
	"eino-cli/deepagent/protocol"
)

type InteractionHandler func(context.Context, protocol.Block) (protocol.Resume, error)

func (r *Runtime) SetInteractionHandler(h InteractionHandler) {
	r.mu.Lock()
	r.interaction = h
	r.mu.Unlock()
}

// ExecuteEvents is the UI adapter. Approval replies resume the saved graph;
// unhandled clarifications are retained and the next user input answers them.
func (r *Runtime) ExecuteEvents(ctx context.Context, prompt string, sink func(protocol.Event)) (legacy.Result, error) {
	input := protocol.Input{Kind: protocol.InputUser, Text: prompt}
	r.mu.Lock()
	block := r.pending
	r.mu.Unlock()
	if block == nil && r.ThreadID() != "" {
		t, err := r.manager.GetThread(ctx, r.ThreadID())
		if err != nil {
			return legacy.Result{}, err
		}
		if t.State == "blocked" {
			block = t.Block
		}
	}
	if block != nil {
		input = resumeInput(*block, protocol.Resume{Answer: prompt, Approved: isApproval(prompt)})
	}
	var output strings.Builder
	for {
		stream, err := r.StartRun(ctx, input)
		if err != nil {
			return legacy.Result{}, err
		}
		if input.Kind == protocol.InputResume {
			r.mu.Lock()
			r.pending = nil
			r.mu.Unlock()
		}
		var waiting *protocol.Block
		var failed error
		responses := map[string]string{}
		var order []string
		for e := range stream.Events {
			if sink != nil {
				sink(e)
			}
			switch e.Kind {
			case protocol.EventText:
				if _, ok := responses[e.ResponseID]; !ok {
					order = append(order, e.ResponseID)
				}
				responses[e.ResponseID] = e.Text
			case protocol.EventBlocked:
				waiting = e.Block
			case protocol.EventRunFailed:
				failed = fmt.Errorf("worker: %s", e.Error)
			case protocol.EventRunCancelled:
				failed = context.Canceled
			}
		}
		err = stream.Err()
		stream.Close()
		if err != nil {
			return legacy.Result{}, err
		}
		if failed != nil {
			return legacy.Result{}, failed
		}
		for _, id := range order {
			if text := strings.TrimSpace(responses[id]); text != "" {
				if output.Len() > 0 {
					output.WriteString("\n\n")
				}
				output.WriteString(text)
			}
		}
		if waiting == nil {
			r.mu.Lock()
			r.pending = nil
			r.mu.Unlock()
			return legacy.Result{Success: true, Output: output.String()}, nil
		}
		r.mu.Lock()
		r.pending = waiting
		handler := r.interaction
		r.mu.Unlock()
		if handler == nil || waiting.Kind == "clarification" || waiting.Kind == "question" {
			if output.Len() > 0 {
				output.WriteString("\n\n")
			}
			output.WriteString(waiting.Question)
			for _, option := range waiting.Options {
				output.WriteString("\n- " + option)
			}
			return legacy.Result{NeedsUser: true, Output: output.String()}, nil
		}
		answer, err := handler(ctx, *waiting)
		if err != nil {
			return legacy.Result{}, err
		}
		input = resumeInput(*waiting, answer)
	}
}
func resumeInput(b protocol.Block, a protocol.Resume) protocol.Input {
	a.RunID = b.RunID
	a.CheckpointID = b.CheckpointID
	a.InterruptID = b.InterruptID
	return protocol.Input{Kind: protocol.InputResume, Resume: &a}
}
func isApproval(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes", "approve", "allow", "同意", "允许":
		return true
	}
	return false
}
func (r *Runtime) ExecuteStream(ctx context.Context, prompt string, onChunk legacy.StreamChunkHandler) (legacy.Result, error) {
	return r.ExecuteEvents(ctx, prompt, func(e protocol.Event) {
		if onChunk != nil && e.Kind == protocol.EventTextDelta {
			onChunk(e.Text)
		}
	})
}
func (r *Runtime) RunDream(context.Context) (legacy.Result, error) {
	return legacy.Result{}, fmt.Errorf("memory consolidation is managed by Worker configuration")
}
func (r *Runtime) ExportHistory() ([]byte, error) {
	events, err := r.History(context.Background())
	if err != nil {
		return nil, err
	}
	return json.Marshal(events)
}
func (r *Runtime) ImportHistory([]byte) error {
	return fmt.Errorf("shared history is owned by Worker; attach using --thread")
}
func (r *Runtime) RollbackToHistory([]byte) error {
	return fmt.Errorf("distributed history does not support local filesystem rollback")
}

func (r *Runtime) Compact(ctx context.Context) (legacy.Result, error) {
	stream, err := r.StartRun(ctx, protocol.Input{Kind: protocol.InputCompact})
	if err != nil {
		return legacy.Result{}, err
	}
	defer stream.Close()
	output := ""
	completed := false
	for e := range stream.Events {
		if e.Kind == protocol.EventRunFailed {
			return legacy.Result{}, fmt.Errorf("compact: %s", e.Error)
		}
		if e.Kind == protocol.EventRunCancelled {
			return legacy.Result{}, context.Canceled
		}
		if e.Kind == protocol.EventCompacted {
			completed = true
			if e.Error != "" {
				return legacy.Result{}, fmt.Errorf("compact: %s", e.Error)
			}
			output = e.Text
		}
	}
	if err = stream.Err(); err != nil {
		return legacy.Result{}, err
	}
	if !completed {
		return legacy.Result{}, fmt.Errorf("compaction ended without a completion event")
	}
	if output == "" {
		output = "Context compacted"
	}
	return legacy.Result{Success: true, Output: output}, nil
}
