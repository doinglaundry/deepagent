package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"eino-cli/backend/runtime/eino"
)

// chunkMsg is one streamed text chunk emitted by the runtime's
// onChunk callback. It's funneled through a buffered channel so
// the runtime goroutine never blocks the bubbletea Update loop.
type chunkMsg string

// doneMsg fires exactly once per submitted prompt, after the
// runtime call returns. err is non-nil on runtime failure (or
// a user-initiated cancel via context.CancelFunc).
type doneMsg struct {
	output string
	err    error
}

// startStream kicks off a runtime.ExecuteStream call in the
// background. It returns:
//   - the chunk channel that successive waitForChunk calls drain;
//   - the cancel func the Update loop calls when the user aborts;
//   - a tea.Cmd that resolves to doneMsg once the runtime call
//     finishes (success or error).
//
// Closing the chunk channel is the goroutine's responsibility —
// waitForChunk treats a closed channel as a no-op (returns nil),
// and the doneMsg fires on its own dedicated channel so it can't
// race with chunk-pump termination.
func startStream(rt eino.Runtime, prompt string) (<-chan string, context.CancelFunc, tea.Cmd) {
	chunkCh := make(chan string, 64)
	doneCh := make(chan doneMsg, 1)
	ctx, cancel := context.WithCancel(context.Background())

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

// waitForChunk reads one chunk from ch and converts it to a
// chunkMsg. On a closed channel it returns nil (which bubbletea
// drops silently), terminating the chunk pump.
func waitForChunk(ch <-chan string) tea.Cmd {
	return func() tea.Msg {
		v, ok := <-ch
		if !ok {
			return nil
		}
		return chunkMsg(v)
	}
}
