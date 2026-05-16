package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/runtime/eino"
)

// chunkMsg is one streamed text chunk from the runtime's onChunk callback.
type chunkMsg string

// doneMsg fires once per prompt after the runtime call returns; err is non-nil
// on failure or user cancel.
type doneMsg struct {
	output string
	err    error
}

// streamConsumer puts trace events on the same queue as chunks and doneMsg, so
// the TUI renders tool calls before the final assistant answer.
type streamConsumer struct {
	ctx context.Context
	ch  chan<- tea.Msg
}

func (c streamConsumer) Send(ev middlewares.TraceEvent) {
	select {
	case c.ch <- ev:
	case <-c.ctx.Done():
	}
}

// startStream runs ExecuteStream in a goroutine. Trace events, text chunks and
// doneMsg share one channel to preserve the model's execution order in the UI.
func startStream(rt eino.Runtime, prompt string) (<-chan tea.Msg, context.CancelFunc) {
	streamCh := make(chan tea.Msg, 64)
	ctx, cancel := context.WithCancel(context.Background())
	ctx = middlewares.WithTraceConsumer(ctx, streamConsumer{ctx: ctx, ch: streamCh})

	go func() {
		defer close(streamCh)
		result, err := rt.ExecuteStream(ctx, prompt, func(chunk string) {
			select {
			case streamCh <- chunkMsg(chunk):
			case <-ctx.Done():
			}
		})
		if err != nil {
			streamCh <- doneMsg{err: err}
			return
		}
		streamCh <- doneMsg{output: result.Output}
	}()
	return streamCh, cancel
}

// waitForStreamMsg reads one ordered runtime event; closed channel ends the pump.
func waitForStreamMsg(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		v, ok := <-ch
		if !ok {
			return nil
		}
		return v
	}
}
