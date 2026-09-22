package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	legacy "eino-cli/deepagent/host/runtime/local"
	"eino-cli/deepagent/protocol"
	"eino-cli/deepagent/session/runs"
	host "eino-cli/deepagent/host/runtime"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
)

type remoteRuntime interface {
	ExecuteEvents(context.Context, string, func(protocol.Event)) (legacy.Result, error)
	Cancel(context.Context) error
	History(context.Context) ([]protocol.Event, error)
	SetInteractionHandler(host.InteractionHandler)
}

func startRemoteStream(r remoteRuntime, prompt string) (<-chan tea.Msg, context.CancelFunc) {
	ch := make(chan tea.Msg, 64)
	var sendMu sync.Mutex
	ctx, detach := context.WithCancel(context.Background())
	go func() {
		defer func() { sendMu.Lock(); close(ch); sendMu.Unlock() }()
		defer detach()
		res, err := r.ExecuteEvents(ctx, prompt, func(e protocol.Event) {
			select {
			case ch <- e:
			case <-ctx.Done():
			}
		})
		select {
		case ch <- doneMsg{output: res.Output, err: err}:
		case <-ctx.Done():
			select {
			case ch <- doneMsg{output: res.Output, err: err}:
			default:
			}
		}
	}()
	cancel := func() {
		go func() {
			controlCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
			defer done()
			if err := r.Cancel(controlCtx); err != nil {
				sendMu.Lock()
				defer sendMu.Unlock()
				if ctx.Err() != nil {
					return
				}
				select {
				case ch <- remoteCancelError{err: err}:
				case <-ctx.Done():
				}
				return
			}
			detach()
		}()
	}
	return ch, cancel
}
func installRemoteApproval(prog *tea.Program, r remoteRuntime) func() {
	r.SetInteractionHandler(func(ctx context.Context, b protocol.Block) (protocol.Resume, error) {
		reply := make(chan bool, 1)
		prog.Send(approvalRequest{toolName: b.ToolName, args: b.Arguments, reply: reply})
		select {
		case yes := <-reply:
			return protocol.Resume{Approved: yes, Answer: fmt.Sprintf("%t", yes)}, nil
		case <-ctx.Done():
			return protocol.Resume{}, ctx.Err()
		}
	})
	return func() { r.SetInteractionHandler(nil) }
}
func applyRemoteEvent(m *Model, e protocol.Event) {
	switch e.Kind {
	case protocol.EventRunStarted:
		// A retry can bind a new run; retain completed scrollback but reset live output.
		m.remoteResponses = map[string]string{}
		m.remoteResponseDone = map[string]bool{}
		m.remoteResponseOrder = nil
		m.streamBuf.Reset()
	case protocol.EventTextDelta, protocol.EventText:
		if m.remoteResponseDone == nil {
			m.remoteResponseDone = map[string]bool{}
		}
		if e.Kind == protocol.EventTextDelta && m.remoteResponseDone[e.ResponseID] {
			return
		}
		if m.remoteResponses == nil {
			m.remoteResponses = map[string]string{}
		}
		if _, ok := m.remoteResponses[e.ResponseID]; !ok {
			m.remoteResponseOrder = append(m.remoteResponseOrder, e.ResponseID)
		}
		if e.Kind == protocol.EventText {
			m.remoteResponses[e.ResponseID] = e.Text
			m.remoteResponseDone[e.ResponseID] = true
		} else {
			m.remoteResponses[e.ResponseID] += e.Text
		}
		m.streamBuf.Reset()
		for i, id := range m.remoteResponseOrder {
			if i > 0 {
				m.streamBuf.WriteString("\n\n")
			}
			m.streamBuf.WriteString(m.remoteResponses[id])
		}
	case protocol.EventToolStarted, protocol.EventToolOutput, protocol.EventToolCompleted:
		if !m.toolBlocksEnabled {
			return
		}
		if m.remoteTools == nil {
			m.remoteTools = map[string]*toolBlock{}
		}
		key := e.RunID + "/" + e.ToolCallID
		if m.remoteToolDone == nil {
			m.remoteToolDone = map[string]bool{}
		}
		if e.Kind == protocol.EventToolOutput && m.remoteToolDone[key] {
			return
		}
		block := m.remoteTools[key]
		if block == nil {
			m.toolBlockSeq++
			block = &toolBlock{id: m.toolBlockSeq, name: e.ToolName, argsLine: formatArgsLine(e.ToolName, e.Arguments, m.toolArgsMaxChars), collapsed: true}
			m.remoteTools[key] = block
			m.toolBlocks = append(m.toolBlocks, block)
			pushToolBlockMessage(m, fmt.Sprintf("%s%d]", toolPlaceholderPrefix, block.id))
		}
		if e.Kind == protocol.EventToolStarted {
			block.lines = []string{"Running…"}
		} else if e.Kind == protocol.EventToolCompleted {
			m.remoteToolDone[key] = true
			block.lines = splitToolLines(e.Text)
			if e.Error != "" {
				block.lines = append(block.lines, e.Error)
			}
		} else {
			block.lines = append(block.lines, splitToolLines(e.Text)...)
		}
		rebuildHistory(m)
	case protocol.EventPlan:
		var todos []deep.TODO
		if json.Unmarshal(e.Data, &todos) != nil {
			var wrapper struct {
				Todos []deep.TODO `json:"todos"`
			}
			if json.Unmarshal(e.Data, &wrapper) == nil {
				todos = wrapper.Todos
			}
		}
		if todos != nil {
			m.todos = todos
			recomputeLayout(m)
		}
	case protocol.EventTokens:
		var tokens struct {
			TotalTokens int64 `json:"total_tokens"`
		}
		if json.Unmarshal(e.Data, &tokens) == nil {
			m.tokenTotal = tokens.TotalTokens
		}
	}
}
func remoteHistory(ctx context.Context, r remoteRuntime) ([]runs.Record, error) {
	events, err := r.History(ctx)
	if err != nil {
		return nil, err
	}
	rows := map[string]*runs.Record{}
	var order []string
	for _, e := range events {
		if e.RunID == "" {
			continue
		}
		row := rows[e.RunID]
		if row == nil {
			row = &runs.Record{ID: e.RunID, SessionID: e.SessionID, CreatedAt: e.CreatedAt, Status: "running"}
			rows[e.RunID] = row
			order = append(order, e.RunID)
		}
		row.UpdatedAt = e.CreatedAt
		switch e.Kind {
		case protocol.EventRunStarted, protocol.EventInputConsumed:
			if e.Text != "" {
				if row.Prompt != "" {
					row.Prompt += "\n"
				}
				row.Prompt += e.Text
			}
		case protocol.EventText:
			if row.Output != "" {
				row.Output += "\n\n"
			}
			row.Output += e.Text
		case protocol.EventRunCompleted:
			row.Status = "success"
		case protocol.EventRunFailed:
			row.Status = "error"
			row.Error = e.Error
		case protocol.EventRunCancelled:
			row.Status = "interrupted"
		case protocol.EventBlocked:
			row.Status = "blocked"
			if e.Block != nil {
				row.Output += "\n" + e.Block.Question
			}
		}
	}
	out := make([]runs.Record, 0, len(order))
	for i := len(order) - 1; i >= 0; i-- {
		out = append(out, *rows[order[i]])
	}
	return out, nil
}
func showRemoteHistoryRow(m *Model) {
	if m.runHistorySel < 0 || m.runHistorySel >= len(m.runHistoryRows) {
		return
	}
	row := m.runHistoryRows[m.runHistorySel]
	closeRunHistory(m)
	if strings.TrimSpace(row.Prompt) != "" {
		pushMessage(m, "user", row.Prompt)
	}
	if strings.TrimSpace(row.Output) != "" {
		pushMessage(m, "assistant", row.Output)
	}
	if row.Error != "" {
		pushMessage(m, "system", row.Error)
	}
}

func handleCompactCommand(m *Model, _ string) tea.Cmd {
	r, ok := m.rt.(interface {
		Compact(context.Context) (legacy.Result, error)
	})
	if !ok {
		pushMessage(m, "system", "Context compaction requires the distributed runtime")
		return nil
	}
	m.streaming = true
	m.streamBuf.Reset()
	m.streamStart = time.Now()
	m.lastErr = nil
	m.interrupted = false
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	ch := make(chan tea.Msg, 1)
	m.streamCh = ch
	go func() {
		defer close(ch)
		defer cancel()
		result, err := r.Compact(ctx)
		ch <- doneMsg{output: result.Output, err: err}
	}()
	return tea.Batch(waitForStreamMsg(ch), m.spin.Tick)
}
func handleCloseCommand(m *Model, _ string) tea.Cmd {
	r, ok := m.rt.(interface{ CloseThread(context.Context) error })
	if !ok {
		pushMessage(m, "system", "Thread closure requires the distributed runtime")
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return closeThreadMsg{err: r.CloseThread(ctx)}
	}
}

type closeThreadMsg struct{ err error }
type remoteCancelError struct{ err error }
