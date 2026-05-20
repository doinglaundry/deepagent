package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"eino-cli/backend/config"
	rt "eino-cli/backend/runtime"
	runtimeRun "eino-cli/backend/runtime/run"
	"eino-cli/backend/session/rollback"
	"eino-cli/backend/session/runs"
)

type historyRuntime struct {
	payload string
}

func (r *historyRuntime) ExecuteStream(context.Context, string, rt.StreamChunkHandler) (rt.Result, error) {
	return rt.Result{}, nil
}

func (r *historyRuntime) ClearHistory() {}

func (r *historyRuntime) ExportHistory() ([]byte, error) { return []byte("[]"), nil }

func (r *historyRuntime) ImportHistory(payload []byte) error {
	r.payload = string(payload)
	return nil
}

func (r *historyRuntime) RollbackToHistory(payload []byte) error {
	return r.ImportHistory(payload)
}

func (r *historyRuntime) SetPlanMode(_ context.Context, on bool) (bool, error) { return on, nil }

func (r *historyRuntime) Name() string { return "history-runtime" }

func TestRunHistoryRenderAndKeys(t *testing.T) {
	m := &Model{
		width:          80,
		runHistoryOpen: true,
		runHistoryRows: []runs.Record{
			{ID: "run-newest", Status: "success", Prompt: "newest prompt", Rollbackable: true},
			{ID: "run-older", Status: "success", Prompt: "older prompt"},
		},
	}
	panel := m.renderRunHistoryPanel()
	if !strings.Contains(panel, "Run history") || !strings.Contains(panel, "newest prompt") {
		t.Fatalf("unexpected history panel:\n%s", panel)
	}
	if _, handled := m.handleRunHistoryKey(tea.KeyMsg{Type: tea.KeyDown}); !handled {
		t.Fatal("down key should be handled")
	}
	if m.runHistorySel != 1 {
		t.Fatalf("selection = %d, want 1", m.runHistorySel)
	}
	if _, handled := m.handleRunHistoryKey(tea.KeyMsg{Type: tea.KeyEsc}); !handled {
		t.Fatal("esc key should be handled")
	}
	if m.runHistoryOpen {
		t.Fatal("history panel should close")
	}
}

func TestRunHistoryRollbackRestoresSelectedPostRun(t *testing.T) {
	root := t.TempDir()
	cleanup := config.SetRootDirForTest(root)
	defer cleanup()
	runStore := runs.NewStore(config.RunsDir())
	rollbackStore := rollback.NewStore(root)
	now := time.Now()
	run1 := runs.Record{
		ID:        "run-1",
		Status:    "success",
		Prompt:    "prompt one",
		Output:    "answer one",
		CreatedAt: now,
		UpdatedAt: now,
	}
	run2 := runs.Record{
		ID:        "run-2",
		Status:    "success",
		Prompt:    "prompt two",
		Output:    "answer two",
		CreatedAt: now.Add(time.Second),
		UpdatedAt: now.Add(time.Second),
	}
	saveRollbackableRecord(t, runStore, rollbackStore, &run1, []byte(`["history-one"]`))
	if err := runStore.Save(context.Background(), run2); err != nil {
		t.Fatal(err)
	}
	saveRollbackableRecord(t, runStore, rollbackStore, &run2, []byte(`["history-two"]`))

	rt := &historyRuntime{}
	m := &Model{
		rt:             rt,
		runs:           runtimeRun.NewManagerWithStore(runStore, rollbackStore),
		width:          80,
		height:         30,
		viewport:       viewport.New(80, 10),
		modelName:      "history-runtime",
		cwd:            root,
		messages:       freshMessages(80, "history-runtime", root),
		runHistoryOpen: true,
		runHistoryRows: []runs.Record{run2, run1},
		runHistorySel:  1,
	}

	m.rollbackSelectedRun()

	if !strings.Contains(rt.payload, "history-one") {
		t.Fatalf("runtime payload = %s", rt.payload)
	}
	body := historyMessageBody(m.messages)
	if !strings.Contains(body, "prompt one") || !strings.Contains(body, "answer one") {
		t.Fatalf("rollback did not rebuild selected history:\n%s", body)
	}
	if strings.Contains(body, "prompt two") || strings.Contains(body, "answer two") {
		t.Fatalf("rollback kept later history:\n%s", body)
	}
	if !strings.Contains(body, "rolled back to run-1") {
		t.Fatalf("rollback confirmation missing:\n%s", body)
	}
}

func saveRollbackableRecord(t *testing.T, runStore *runs.Store, rollbackStore *rollback.Store, rec *runs.Record, history []byte) {
	t.Helper()
	if err := runStore.Save(context.Background(), *rec); err != nil {
		t.Fatal(err)
	}
	path, err := rollbackStore.SavePost(context.Background(), rec.ID, history)
	if err != nil {
		t.Fatal(err)
	}
	rec.Rollbackable = true
	rec.RollbackPath = path
	if err := runStore.Save(context.Background(), *rec); err != nil {
		t.Fatal(err)
	}
	if err := runs.NewStore(filepath.Join(path, "runs")).Save(context.Background(), *rec); err != nil {
		t.Fatal(err)
	}
}

func historyMessageBody(messages []chatMessage) string {
	var b strings.Builder
	for _, msg := range messages {
		b.WriteString(msg.Content)
		b.WriteString("\n")
	}
	return b.String()
}
