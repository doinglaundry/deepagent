package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	rt "eino-cli/backend/runtime"
	runtimeRun "eino-cli/backend/runtime/run"
)

type chunkMsg string

type doneMsg struct {
	output string
	err    error
}

func startStream(runtime rt.Runtime, prompt string, runs *runtimeRun.Manager) (<-chan tea.Msg, context.CancelFunc) {
	streamCh := make(chan tea.Msg, 64)
	events, cancel, err := runtimeRun.Start(context.Background(), runtime, prompt, runs)
	if err != nil {
		streamCh <- doneMsg{err: err}
		close(streamCh)
		return streamCh, func() {}
	}
	go consumeRunEvents(streamCh, events)
	return streamCh, cancel
}

func consumeRunEvents(streamCh chan<- tea.Msg, events <-chan runtimeRun.Event) {
	defer close(streamCh)
	for ev := range events {
		switch ev.Type {
		case runtimeRun.EventChunk:
			streamCh <- chunkMsg(ev.Chunk)
		case runtimeRun.EventTrace:
			if ev.Trace != nil {
				streamCh <- *ev.Trace
			}
		case runtimeRun.EventDone:
			streamCh <- doneMsg{output: ev.Output}
		case runtimeRun.EventError:
			streamCh <- doneMsg{err: ev.Err}
		}
	}
}

func waitForStreamMsg(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		v, ok := <-ch
		if !ok {
			return nil
		}
		return v
	}
}
