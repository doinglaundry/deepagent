package tui

import (
	"bytes"
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"eino-cli/backend/agent"
	"eino-cli/backend/runtime/eino"
)

// stubRuntime satisfies eino.Runtime without touching a real LLM. Only
// Name() is exercised by these tests (Model.New reads it for the header
// and welcome card); the others would only fire if /clear or a prompt
// submission ran, which they don't.
type stubRuntime struct{}

func (stubRuntime) Execute(ctx context.Context, prompt string) (eino.Result, error) {
	return eino.Result{}, nil
}
func (stubRuntime) ExecuteStream(ctx context.Context, prompt string, onChunk eino.StreamChunkHandler) (eino.Result, error) {
	return eino.Result{}, nil
}
func (stubRuntime) ClearHistory() {}
func (stubRuntime) Name() string  { return "stub-model" }

// runHITLE2E spins up the TUI inside a real *tea.Program (via teatest)
// with installTUIApproval wired to that program, kicks off a goroutine
// that calls agent.HITLApprover (the way middlewares.HITL would), and
// returns a channel that the caller drains for the final decision.
//
// Save/restore of agent.HITLApprover keeps these e2e tests from
// polluting the rest of the suite — the unit tests in hitl_test.go
// touch *Model directly and don't install anything, but a defensive
// cleanup is cheaper than debugging a flake.
func runHITLE2E(t *testing.T) (*teatest.TestModel, <-chan bool) {
	t.Helper()

	prev := agent.HITLApprover
	t.Cleanup(func() { agent.HITLApprover = prev })

	m, err := New(stubRuntime{})
	if err != nil {
		t.Fatalf("New(stubRuntime): %v", err)
	}

	// 120x40 is wide enough for the boxed welcome card path; narrower
	// would land us in the compact fallback. Either works for HITL but
	// 120 matches what most users see.
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 40))
	installTUIApproval(tm.GetProgram())

	decisionCh := make(chan bool, 1)
	go func() {
		// 5s ceiling so a stuck approver doesn't leak a goroutine past
		// the test — the test itself uses a tighter 2s WaitFor below.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		decisionCh <- agent.HITLApprover(ctx, "execute", `{"command":"pwd"}`)
	}()

	// Wait for the panel to actually render before the test sends a
	// keystroke; otherwise tm.Type races against handleApprovalRequest
	// and the key arrives while m.hitlQueue is still empty (the input
	// would swallow it as plain text).
	teatest.WaitFor(t, tm.Output(),
		func(out []byte) bool {
			return bytes.Contains(out, []byte(`Approve tool "execute"`))
		},
		teatest.WithCheckInterval(20*time.Millisecond),
		teatest.WithDuration(2*time.Second),
	)
	return tm, decisionCh
}

// expectDecision drains the reply chan with a short ceiling so a
// missed-resolution bug surfaces as a clear test failure instead of a
// goroutine hang.
func expectDecision(t *testing.T, ch <-chan bool, want bool) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Errorf("approver returned %v, want %v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("approver did not return within 2s; want %v", want)
	}
}

// TestHITL_E2E_ApproveYRoutesThroughTeaProgram is the integration the
// unit tests can't cover: an actual *tea.Program receives an
// approvalRequest sent by the installed approver, renders the panel,
// gets a real keystroke, and the approver's blocking call returns true.
//
// Together with the unit tests this pins down the full bubbletea path:
//   - installTUIApproval → prog.Send routes a tea.Msg correctly
//   - Update → handleApprovalRequest enqueues + relayouts
//   - View → renderApprovalPanel actually appears on screen
//   - keyboard → handleApprovalKey wins over input/popup at the
//     program level (not just the unit-test-level bypass)
//   - resolveApproval → reply chan delivers to the blocked approver
func TestHITL_E2E_ApproveYRoutesThroughTeaProgram(t *testing.T) {
	tm, decisionCh := runHITLE2E(t)

	tm.Type("y")
	expectDecision(t, decisionCh, true)

	tm.Quit()
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestHITL_E2E_DenyEnterRoutesThroughTeaProgram pins the denial alias:
// pressing Enter on the prompt resolves with false. Mirrors the stdin
// scanner's "EOF or anything not literally y/yes denies" default, kept
// the same way in the TUI so the user habit transfers between modes.
func TestHITL_E2E_DenyEnterRoutesThroughTeaProgram(t *testing.T) {
	tm, decisionCh := runHITLE2E(t)

	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	expectDecision(t, decisionCh, false)

	tm.Quit()
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestHITL_E2E_DenyNRoutesThroughTeaProgram pins the explicit-n path
// alongside the implicit-Enter one above. Keeping both as separate
// tests means a regression in Enter mapping doesn't silently hide
// behind 'n' still working (or vice versa).
func TestHITL_E2E_DenyNRoutesThroughTeaProgram(t *testing.T) {
	tm, decisionCh := runHITLE2E(t)

	tm.Type("n")
	expectDecision(t, decisionCh, false)

	tm.Quit()
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}
