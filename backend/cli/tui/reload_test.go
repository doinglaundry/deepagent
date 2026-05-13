package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/viewport"

	"eino-cli/backend/runtime/eino"
)

type reloadRuntime struct {
	reloaded bool
}

func (r *reloadRuntime) Execute(context.Context, string) (eino.Result, error) {
	return eino.Result{}, nil
}

func (r *reloadRuntime) ExecuteStream(context.Context, string, eino.StreamChunkHandler) (eino.Result, error) {
	return eino.Result{}, nil
}

func (r *reloadRuntime) ClearHistory() {}

func (r *reloadRuntime) ReloadSoul(context.Context) error {
	r.reloaded = true
	return nil
}

func (r *reloadRuntime) Name() string { return "stub-model" }

func TestReloadCommandVisibleInPopupAndHelp(t *testing.T) {
	got := filterCommands(commands, "/re")
	if len(got) != 1 || got[0].Name != "reload" {
		t.Fatalf("expected reload command, got %#v", got)
	}
	if !strings.Contains(builtinHelp(), "/reload") {
		t.Fatalf("builtinHelp missing /reload:\n%s", builtinHelp())
	}
}

func TestHandleReloadCmdReloadsRuntimeAndResetsView(t *testing.T) {
	rt := &reloadRuntime{}
	m, err := New(rt)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.width = 80
	m.height = 24
	m.viewport = viewport.New(80, 10)
	m.messages = []chatMessage{{Role: "user", Content: "old"}}
	m.toolBlocks = []*toolBlock{{id: 1, lines: []string{"old"}}}
	m.lastSeenMsgCount = 3
	m.toolBlockSeq = 1
	m.footerHint = "nothing to expand"

	cmd, handled := m.handleBuiltin("/reload")
	if !handled {
		t.Fatal("/reload should be handled")
	}
	if cmd == nil {
		t.Fatal("/reload should return a command")
	}
	msg, ok := cmd().(reloadDoneMsg)
	if !ok {
		t.Fatalf("unexpected command msg: %#v", msg)
	}
	if !rt.reloaded {
		t.Fatal("ReloadSoul was not called")
	}

	_, _ = m.handleReloadDone(msg)
	if len(m.toolBlocks) != 0 || m.lastSeenMsgCount != 0 || m.toolBlockSeq != 0 || m.footerHint != "" {
		t.Fatalf("reload did not reset tool state: %#v", m)
	}
	if !strings.Contains(m.messages[len(m.messages)-1].Content, "reloaded") {
		t.Fatalf("missing reload success message: %#v", m.messages)
	}
}

func TestHandleReloadCmdRejectsDuringBootstrap(t *testing.T) {
	rt := &reloadRuntime{}
	m, err := New(rt)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.bootstrapLoading = true

	cmd, handled := m.handleBuiltin("/reload")
	if !handled {
		t.Fatal("/reload should be handled")
	}
	if cmd != nil {
		t.Fatal("/reload should not run while bootstrap is loading")
	}
	if rt.reloaded {
		t.Fatal("ReloadSoul should not be called")
	}
}
