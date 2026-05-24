package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// hitlTestModel returns a *Model wired enough for the HITL handlers:
// a real width/height so recomputeLayout doesn't short-circuit, but no
// runtime / textinput plumbing — these tests touch hitl state only.
func hitlTestModel() *Model {
	return &Model{width: 100, height: 40, ready: true}
}

// enqueueRequest builds a fresh approvalRequest and feeds it through
// handleApprovalRequest the same way Update would. Returns the reply
// chan so the test can assert what (if anything) was sent.
func enqueueRequest(t *testing.T, m *Model, name, args string) chan bool {
	t.Helper()
	reply := make(chan bool, 1)
	applyApprovalRequest(m,approvalRequest{toolName: name, args: args, reply: reply})
	return reply
}

// recvDecision waits up to 50ms for a value on reply. Anything longer
// is a deadlock indicator — handleApprovalKey is supposed to push the
// decision synchronously, no goroutine in the loop.
func recvDecision(t *testing.T, reply chan bool) (bool, bool) {
	t.Helper()
	select {
	case v := <-reply:
		return v, true
	case <-time.After(50 * time.Millisecond):
		return false, false
	}
}

func TestHandleApprovalRequest_EnqueuesAndShowsPanel(t *testing.T) {
	m := hitlTestModel()
	if got := renderApprovalPanel(m); got != "" {
		t.Fatalf("empty queue must render no panel, got %q", got)
	}

	_ = enqueueRequest(t, m, "shell", `{"cmd":"ls"}`)
	if len(m.hitlQueue) != 1 {
		t.Fatalf("expected 1 queued, got %d", len(m.hitlQueue))
	}

	panel := renderApprovalPanel(m)
	if !strings.Contains(panel, `"shell"`) {
		t.Errorf("panel must name the tool; got:\n%s", panel)
	}
	if !strings.Contains(panel, "y = allow") {
		t.Errorf("panel must show the y/n hint; got:\n%s", panel)
	}
}

func TestView_ApprovalReplacesInputFooterAndThinking(t *testing.T) {
	ti := textinput.New()
	ti.Placeholder = "Ask anything... (/help for commands)"
	m := &Model{
		width:       100,
		height:      40,
		ready:       true,
		streaming:   true,
		input:       ti,
		viewport:    viewport.New(100, 5),
		verbPresent: "Scheming",
		hitlQueue: []approvalRequest{{
			toolName: "execute",
			args:     `{"command":"pwd"}`,
			reply:    make(chan bool, 1),
		}},
	}

	got := m.View()
	for _, hidden := range []string{"Ask anything", "esc to interrupt", "thinking"} {
		if strings.Contains(got, hidden) {
			t.Fatalf("approval view must hide %q; got:\n%s", hidden, got)
		}
	}
	if !strings.Contains(got, `Approve tool "execute"`) {
		t.Fatalf("approval view missing prompt; got:\n%s", got)
	}
}

func TestHandleApprovalKey_YApprovesAndPops(t *testing.T) {
	m := hitlTestModel()
	reply := enqueueRequest(t, m, "shell", "{}")

	cmd, handled := applyApprovalKey(m,tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if !handled || cmd != nil {
		t.Errorf("y should resolve & swallow; got cmd=%v handled=%v", cmd, handled)
	}
	got, ok := recvDecision(t, reply)
	if !ok {
		t.Fatal("approver never received a decision")
	}
	if got != true {
		t.Errorf("y must approve; got %v", got)
	}
	if len(m.hitlQueue) != 0 {
		t.Errorf("queue should pop after resolution; len=%d", len(m.hitlQueue))
	}
}

func TestHandleApprovalKey_DenialAliases(t *testing.T) {
	cases := []struct {
		name string
		key  tea.KeyMsg
	}{
		{"n_lowercase", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}}},
		{"N_uppercase", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}}},
		{"Enter", tea.KeyMsg{Type: tea.KeyEnter}},
		{"Esc", tea.KeyMsg{Type: tea.KeyEsc}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := hitlTestModel()
			reply := enqueueRequest(t, m, "shell", "{}")

			_, handled := applyApprovalKey(m,tc.key)
			if !handled {
				t.Errorf("%s should be handled", tc.name)
			}
			got, ok := recvDecision(t, reply)
			if !ok {
				t.Fatal("no decision delivered")
			}
			if got != false {
				t.Errorf("%s must deny; got %v", tc.name, got)
			}
		})
	}
}

func TestHandleApprovalKey_CtrlCDeniesAndFallsThrough(t *testing.T) {
	// Contract: Ctrl-C resolves the front (deny) but returns
	// handled=false so the outer KeyCtrlC chain still fires (which
	// aborts the streaming turn). This is what guarantees a single
	// Ctrl-C both denies and tears down the turn.
	m := hitlTestModel()
	reply := enqueueRequest(t, m, "shell", "{}")

	_, handled := applyApprovalKey(m,tea.KeyMsg{Type: tea.KeyCtrlC})
	if handled {
		t.Error("Ctrl-C must NOT swallow handled=true (outer abort needs to run)")
	}
	got, ok := recvDecision(t, reply)
	if !ok || got != false {
		t.Errorf("Ctrl-C must deny synchronously; got=%v ok=%v", got, ok)
	}
}

func TestHandleKey_CtrlCDuringApprovalDeniesAndAborts(t *testing.T) {
	m := hitlTestModel()
	m.streaming = true
	cancelled := false
	m.cancel = func() { cancelled = true }
	reply := enqueueRequest(t, m, "shell", "{}")

	_, cmd := applyKey(m,tea.KeyMsg{Type: tea.KeyCtrlC})
	_ = cmd
	got, ok := recvDecision(t, reply)
	if !ok || got != false {
		t.Fatalf("Ctrl-C must deny synchronously; got=%v ok=%v", got, ok)
	}
	if !cancelled {
		t.Fatal("Ctrl-C during approval must abort the streaming turn")
	}
	if len(m.hitlQueue) != 0 {
		t.Fatalf("approval queue should be drained after abort; len=%d", len(m.hitlQueue))
	}
}

func TestHandleApprovalKey_UnrelatedKeyIgnored(t *testing.T) {
	m := hitlTestModel()
	reply := enqueueRequest(t, m, "shell", "{}")

	_, handled := applyApprovalKey(m,tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if handled {
		t.Error("unrelated keys must not be 'handled' (caller still sees them)")
	}
	if _, ok := recvDecision(t, reply); ok {
		t.Error("unrelated key must NOT resolve the request")
	}
	if len(m.hitlQueue) != 1 {
		t.Errorf("queue must remain intact; len=%d", len(m.hitlQueue))
	}
}

func TestHitl_FIFO_ResolvesInOrder(t *testing.T) {
	m := hitlTestModel()
	r1 := enqueueRequest(t, m, "shell", `{"cmd":"first"}`)
	r2 := enqueueRequest(t, m, "shell", `{"cmd":"second"}`)
	if len(m.hitlQueue) != 2 {
		t.Fatalf("len=%d, want 2", len(m.hitlQueue))
	}

	// First decision: y. Should resolve r1 with true; r2 still queued.
	applyApprovalKey(m,tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if v, ok := recvDecision(t, r1); !ok || v != true {
		t.Errorf("r1 should be approved; got=%v ok=%v", v, ok)
	}
	if _, ok := recvDecision(t, r2); ok {
		t.Error("r2 must still be pending after first y")
	}
	if len(m.hitlQueue) != 1 {
		t.Errorf("queue len after first resolution = %d, want 1", len(m.hitlQueue))
	}

	// Panel must now show the second request's args (FIFO front swap).
	if got := renderApprovalPanel(m); !strings.Contains(got, "second") {
		t.Errorf("panel after first resolution must show 'second' request; got:\n%s", got)
	}

	// Second decision: n. Should resolve r2 with false; queue empty.
	applyApprovalKey(m,tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if v, ok := recvDecision(t, r2); !ok || v != false {
		t.Errorf("r2 should be denied; got=%v ok=%v", v, ok)
	}
	if len(m.hitlQueue) != 0 {
		t.Errorf("queue should be empty after both resolutions; len=%d", len(m.hitlQueue))
	}
	if got := renderApprovalPanel(m); got != "" {
		t.Errorf("panel must vanish after queue drains; got:\n%s", got)
	}
}

func TestDrainApprovals_ClearsQueueWithoutSending(t *testing.T) {
	// Contract: the approver's ctx-done branch is what returns false
	// when a turn unwinds; drainApprovals only clears the UI state.
	// We verify by NOT receiving anything on the reply channels.
	m := hitlTestModel()
	r1 := enqueueRequest(t, m, "shell", "{}")
	r2 := enqueueRequest(t, m, "shell", "{}")

	drainApprovals(m)

	if len(m.hitlQueue) != 0 {
		t.Errorf("drain must empty the queue; len=%d", len(m.hitlQueue))
	}
	if _, ok := recvDecision(t, r1); ok {
		t.Error("drain must NOT send to r1 (ctx-done branch handles it)")
	}
	if _, ok := recvDecision(t, r2); ok {
		t.Error("drain must NOT send to r2 (ctx-done branch handles it)")
	}
}

func TestHandleKey_PendingBlocksUnrelatedInput(t *testing.T) {
	// While the queue is non-empty handleKey must drop unrelated keys
	// (no input echo, no submit) to keep the next keystroke an
	// unambiguous decision. Resolution keys are tested above; this one
	// pins the "everything else dies here" half of the contract.
	m := hitlTestModel()
	_ = enqueueRequest(t, m, "shell", "{}")

	_, cmd := applyKey(m,tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h', 'i'}})
	// We don't strictly assert cmd==nil (handleKey may return tea.Batch
	// of nils internally) but the queue must be intact.
	_ = cmd
	if len(m.hitlQueue) != 1 {
		t.Errorf("queue should not change on unrelated input; len=%d", len(m.hitlQueue))
	}
}
