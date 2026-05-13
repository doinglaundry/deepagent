package tui

import (
	"strings"
	"testing"

	"eino-cli/backend/config"
)

func TestCommandsIncludesBootstrap(t *testing.T) {
	got := filterCommands(commands, "/boot")
	if len(got) != 1 || got[0].Name != "bootstrap" {
		t.Fatalf("expected bootstrap command, got %#v", got)
	}
	if !strings.Contains(builtinHelp(), "/bootstrap") {
		t.Fatalf("builtinHelp missing /bootstrap:\n%s", builtinHelp())
	}
}

func TestHandleBootstrapCmdStartsMode(t *testing.T) {
	m, err := New(stubRuntime{}, &config.Config{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cmd, handled := m.handleBuiltin("/bootstrap")
	if !handled {
		t.Fatal("/bootstrap should be handled")
	}
	if cmd == nil {
		t.Fatal("/bootstrap should return command for first bootstrap reply")
	}
	if m.bootstrap == nil {
		t.Fatal("bootstrap session not started")
	}
}

func TestSubmitBootstrapCancel(t *testing.T) {
	m, err := New(stubRuntime{}, &config.Config{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cmd, _ := m.handleBuiltin("/bootstrap")
	if cmd == nil || m.bootstrap == nil {
		t.Fatal("bootstrap session not started")
	}

	_, cmd = m.submitBootstrap("/cancel")
	if cmd != nil {
		t.Fatal("/cancel should not return command")
	}
	if m.bootstrap != nil {
		t.Fatal("/cancel should clear bootstrap session")
	}
}
