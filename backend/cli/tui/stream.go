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

// teaProgramConsumer adapts *tea.Program to middlewares.TraceConsumer; bubbletea
// drops Sends silently after stop, so no panic / no block.
type teaProgramConsumer struct{ p *tea.Program }

func (c teaProgramConsumer) Send(ev middlewares.TraceEvent) {
	c.p.Send(ev)
}

// startStream runs ExecuteStream in a goroutine, returning the chunk channel,
// a cancel func, and a tea.Cmd that resolves to doneMsg. consumer=nil disables tracing.
func startStream(rt eino.Runtime, prompt string, consumer middlewares.TraceConsumer) (<-chan string, context.CancelFunc, tea.Cmd) {
	chunkCh := make(chan string, 64)
	doneCh := make(chan doneMsg, 1)
	ctx, cancel := context.WithCancel(context.Background())
	ctx = middlewares.WithTraceConsumer(ctx, consumer)

	go func() {
		defer close(chunkCh)
		result, err := rt.ExecuteStream(ctx, prompt, func(chunk string) {
			select {
			case chunkCh <- chunk:
			case <-ctx.Done():
			}
		})
		if err != nil {
			doneCh <- doneMsg{err: err}
			return
		}
		doneCh <- doneMsg{output: result.Output}
	}()

	awaitDone := func() tea.Msg { return <-doneCh }
	return chunkCh, cancel, awaitDone
}

// waitForChunk reads one chunk; closed channel returns nil and ends the pump.
func waitForChunk(ch <-chan string) tea.Cmd {
	return func() tea.Msg {
		v, ok := <-ch
		if !ok {
			return nil
		}
		return chunkMsg(v)
	}
}
