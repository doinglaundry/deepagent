package main

import (
	"path/filepath"
	"testing"
)

func TestParseFlagsRootPrefersFlag(t *testing.T) {
	root, mode, addr, err := parseFlags(
		[]string{"--root", "from-flag"},
		func(string) string { return "from-env" },
		func() (string, error) { return "from-wd", nil },
	)
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
	root, _, _, err := parseFlags(
		nil,
		func(key string) string {
			if key == "SGADK_ROOT" {
				return "from-env"
			}
			return ""
		},
		func() (string, error) { return "from-wd", nil },
	)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want, _ := filepath.Abs("from-env")
	if root != want {
		t.Fatalf("got %q, want %q", root, want)
	}
}

func TestParseFlagsRootFallsBackToWorkingDirectory(t *testing.T) {
	root, _, _, err := parseFlags(
		nil,
		func(string) string { return "" },
		func() (string, error) { return "from-wd", nil },
	)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want, _ := filepath.Abs("from-wd")
	if root != want {
		t.Fatalf("got %q, want %q", root, want)
	}
}

func TestParseFlagsServerMode(t *testing.T) {
	_, mode, addr, err := parseFlags(
		[]string{"--mode", "server", "--addr", ":9000"},
		func(string) string { return "" },
		func() (string, error) { return "/tmp", nil },
	)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if mode != "server" || addr != ":9000" {
		t.Fatalf("got mode=%q addr=%q", mode, addr)
	}
}
