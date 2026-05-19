package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

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

// startStream starts a runtime run and converts run events into Bubble Tea messages.
func startStream(rt eino.Runtime, prompt string, runs *eino.RunManager) (<-chan tea.Msg, context.CancelFunc) {
	streamCh := make(chan tea.Msg, 64)
	events, cancel, err := eino.StartRun(context.Background(), rt, prompt, runs)
	if err != nil {
		streamCh <- doneMsg{err: err}
		close(streamCh)
		return streamCh, func() {}
	}
	go consumeRunEvents(streamCh, events)
	return streamCh, cancel
}

func consumeRunEvents(streamCh chan<- tea.Msg, events <-chan eino.RunEvent) {
	defer close(streamCh)
	for ev := range events {
		switch ev.Type {
		case eino.RunEventChunk:
			streamCh <- chunkMsg(ev.Chunk)
		case eino.RunEventTrace:
			if ev.Trace != nil {
				streamCh <- *ev.Trace
			}
		case eino.RunEventDone:
			streamCh <- doneMsg{output: ev.Output}
		case eino.RunEventError:
			streamCh <- doneMsg{err: ev.Err}
		}
	}
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
