package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eino-cli/backend/config"
)

func TestParseFlagsRootPrefersFlag(t *testing.T) {
	t.Setenv("SGADK_ROOT", "from-env")
	root, mode, addr, err := parseFlags([]string{"--root", "from-flag"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want, _ := filepath.Abs("from-flag")
	if root != want {
		t.Fatalf("root: got %q, want %q", root, want)
	}
	if mode != "cli" {
		t.Fatalf("default mode: got %q, want cli", mode)
	}
	if addr != ":8000" {
		t.Fatalf("default addr: got %q, want :8000", addr)
	}
}

func TestParseFlagsRootFallsBackToEnv(t *testing.T) {
	t.Setenv("SGADK_ROOT", "from-env")
	root, _, _, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want, _ := filepath.Abs("from-env")
	if root != want {
		t.Fatalf("got %q, want %q", root, want)
	}
}

func TestParseFlagsRootFallsBackToWorkingDirectory(t *testing.T) {
	t.Setenv("SGADK_ROOT", "")
	root, _, _, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want, _ = filepath.Abs(want)
	if root != want {
		t.Fatalf("got %q, want %q", root, want)
	}
}

func TestParseFlagsServerMode(t *testing.T) {
	_, mode, addr, err := parseFlags([]string{"--mode", "server", "--addr", ":9000"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if mode != "server" || addr != ":9000" {
		t.Fatalf("got mode=%q addr=%q", mode, addr)
	}
}

func TestBuildSandboxManagerDefaultsToLocal(t *testing.T) {
	manager, err := buildSandboxManager(&config.Config{})
	if err != nil {
		t.Fatalf("buildSandboxManager: %v", err)
	}
	if manager == nil {
		t.Fatal("manager is nil")
	}
}

func TestBuildSandboxManagerRejectsUnknownUse(t *testing.T) {
	_, err := buildSandboxManager(&config.Config{Sandbox: config.SandboxConfig{Use: "bad"}})
	if err == nil {
		t.Fatal("expected unknown sandbox.use error")
	}
	if !strings.Contains(err.Error(), "sandbox.use") {
		t.Fatalf("error should mention sandbox.use, got %v", err)
	}
}

func TestResetAgentMessagesLogClearsExistingFile(t *testing.T) {
	root := t.TempDir()
	restore := config.SetRootDirForTest(root)
	t.Cleanup(restore)

	path := config.AgentMessagesLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old messages"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := resetAgentMessagesLog(); err != nil {
		t.Fatalf("resetAgentMessagesLog: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("log should be empty after reset, got %q", data)
	}
}
